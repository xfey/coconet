package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
)

const (
	DefaultCommand = "claude"
	adapterVersion = "8"
)

type Adapter struct {
	Command        string
	NodeCommand    string
	HelperPath     string
	SDKEntry       string
	TranscriptPath string
	ConfigRoot     string
	ClientVersion  string
	Locate         adapters.CommandLocator
	run            helperRunner
}

type helperRequest struct {
	Action               string `json:"action"`
	TranscriptPath       string `json:"transcript_path,omitempty"`
	NativeSessionID      string `json:"native_session_id,omitempty"`
	SourceArtifactPath   string `json:"source_artifact_path,omitempty"`
	SourceArtifactSHA256 string `json:"source_artifact_sha256,omitempty"`
	AttemptDirectory     string `json:"attempt_directory,omitempty"`
	CWD                  string `json:"cwd"`
	ConfigRoot           string `json:"config_root"`
	ClientVersion        string `json:"client_version,omitempty"`
	AdapterVersion       string `json:"adapter_version"`
}

type helperRunner func(context.Context, helperRequest) ([]byte, error)

func New(command string) Adapter {
	if command == "" {
		command = DefaultCommand
	}
	return Adapter{
		Command:       command,
		NodeCommand:   defaultNodeCommand(),
		HelperPath:    defaultHelperPath(),
		SDKEntry:      defaultSDKEntry(),
		ConfigRoot:    defaultConfigRoot(),
		ClientVersion: environmentOr("COCONET_CLAUDE_VERSION", "unknown"),
	}
}

func (Adapter) Type() contracts.AgentType { return contracts.AgentClaude }

func (adapter Adapter) Health(context.Context) contracts.ComponentHealth {
	return adapters.ExecutableHealth("adapter.claude", adapter.Command, adapter.Locate)
}

func (adapter Adapter) Discover(ctx context.Context, root string) ([]contracts.DiscoveredSession, error) {
	content, err := adapter.runHelper(ctx, helperRequest{Action: "discover", CWD: root, ConfigRoot: adapter.ConfigRoot, ClientVersion: adapter.ClientVersion, AdapterVersion: adapterVersion})
	if err != nil {
		return nil, err
	}
	var response contracts.SessionDiscovery
	if err := json.Unmarshal(content, &response); err != nil {
		return nil, fmt.Errorf("decode Claude discovery: %w", err)
	}
	if response.SchemaVersion != contracts.SchemaVersion {
		return nil, errors.New("Claude helper returned an unsupported discovery schema")
	}
	filtered := make([]contracts.DiscoveredSession, 0, len(response.Sessions))
	for _, session := range response.Sessions {
		if session.AgentType == contracts.AgentClaude && session.NativeSessionID != "" && session.CWD != "" && adapters.PathContains(root, session.CWD) {
			filtered = append(filtered, session)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].UpdatedAt > filtered[j].UpdatedAt })
	if response.Truncated {
		return filtered, adapters.ErrDiscoveryLimit
	}
	return filtered, nil
}

func (adapter Adapter) Observe(ctx context.Context, cwd, nativeSessionID string, checkpoint adapters.ObservationCheckpoint) (result contracts.SessionObservation, next adapters.ObservationCheckpoint, failure error) {
	defer adapters.TrackCaptureFailure(&failure, []string{adapter.TranscriptPath})()
	content, err := adapter.runHelper(ctx, helperRequest{Action: "observe", TranscriptPath: adapter.TranscriptPath, NativeSessionID: nativeSessionID, CWD: cwd, ConfigRoot: adapter.ConfigRoot, ClientVersion: adapter.ClientVersion, AdapterVersion: adapterVersion})
	if err != nil {
		return contracts.SessionObservation{}, adapters.ObservationCheckpoint{}, err
	}
	var observation contracts.SessionObservation
	if err := json.Unmarshal(content, &observation); err != nil {
		return contracts.SessionObservation{}, adapters.ObservationCheckpoint{}, fmt.Errorf("decode Claude observation: %w", err)
	}
	if observation.SchemaVersion != contracts.SchemaVersion || observation.Session.AgentType != contracts.AgentClaude || observation.Session.NativeSessionID != nativeSessionID || observation.Session.CWD == "" {
		return contracts.SessionObservation{}, adapters.ObservationCheckpoint{}, errors.New("Claude helper returned an unexpected Session identity")
	}
	if len(observation.Normalized.Turns) == 0 && len(checkpoint.TurnFingerprints) != 0 {
		return contracts.SessionObservation{}, adapters.ObservationCheckpoint{}, errors.New("Claude SDK returned an empty complete message chain for a previously populated Session; retry without advancing the checkpoint")
	}
	normalized, nextCheckpoint := adapters.ApplyCheckpoint(observation.Normalized.Turns, checkpoint)
	if observation.VisibleText != nil {
		observation.VisibleText.SourceCursor = normalized.ToCursor
		if err := adapters.ValidateVisibleText(observation.VisibleText, observation.Normalized.Turns, normalized.ToCursor); err != nil {
			return contracts.SessionObservation{}, adapters.ObservationCheckpoint{}, err
		}
	}
	observation.Normalized = normalized
	return observation, nextCheckpoint, nil
}

func (adapter Adapter) Fork(ctx context.Context, request adapters.ForkRequest) (adapters.ForkResult, error) {
	if !adapters.ValidNativeSessionID(request.SourceNativeSessionID) || request.SourceArtifactPath == "" || request.SourceArtifactSHA256 == "" || request.CWD == "" || request.AttemptDirectory == "" {
		return adapters.ForkResult{}, errors.New("Claude Fork request is incomplete")
	}
	hash, _, err := adapters.HashFile(request.SourceArtifactPath)
	if err != nil || hash != request.SourceArtifactSHA256 {
		return adapters.ForkResult{}, errors.New("Claude Fork source artifact failed hash validation")
	}
	if marked, exists, err := adapters.LoadForkMarker(request, contracts.AgentClaude); err != nil {
		return adapters.ForkResult{}, err
	} else if exists {
		if err := adapter.verifyFork(ctx, request.CWD, marked.NativeSessionID); err != nil {
			return adapters.ForkResult{}, fmt.Errorf("verify recorded Claude Fork: %w", err)
		}
		return marked, nil
	}
	content, err := adapter.runHelper(ctx, helperRequest{
		Action: "fork", NativeSessionID: request.SourceNativeSessionID,
		SourceArtifactPath: request.SourceArtifactPath, SourceArtifactSHA256: request.SourceArtifactSHA256,
		AttemptDirectory: request.AttemptDirectory, CWD: request.CWD, ConfigRoot: adapter.ConfigRoot,
		ClientVersion: adapter.ClientVersion, AdapterVersion: adapterVersion,
	})
	if err != nil {
		return adapters.ForkResult{}, err
	}
	var result adapters.ForkResult
	if err := json.Unmarshal(content, &result); err != nil || result.NativeSessionID == "" || result.NativeSessionID == request.SourceNativeSessionID {
		return adapters.ForkResult{}, errors.New("Claude helper returned an invalid Fork identity")
	}
	if err := adapters.SaveForkMarker(request, contracts.AgentClaude, result); err != nil {
		return adapters.ForkResult{}, err
	}
	if err := adapter.verifyFork(ctx, request.CWD, result.NativeSessionID); err != nil {
		return adapters.ForkResult{}, err
	}
	return result, nil
}

func (adapter Adapter) ResumeCommand(nativeSessionID string) contracts.ResumeCommand {
	args := []string{"--resume", nativeSessionID}
	return contracts.ResumeCommand{Program: adapter.Command, Args: args, Display: strings.Join(append([]string{adapter.Command}, args...), " ")}
}

func (adapter Adapter) verifyFork(ctx context.Context, cwd, nativeSessionID string) error {
	content, err := adapter.runHelper(ctx, helperRequest{
		Action: "verify_fork", NativeSessionID: nativeSessionID, CWD: cwd,
		ConfigRoot: adapter.ConfigRoot, ClientVersion: adapter.ClientVersion, AdapterVersion: adapterVersion,
	})
	if err != nil {
		return err
	}
	var result adapters.ForkResult
	if err := json.Unmarshal(content, &result); err != nil || result.NativeSessionID != nativeSessionID {
		return errors.New("Claude helper could not verify the new Fork")
	}
	return nil
}

func (adapter Adapter) runHelper(ctx context.Context, request helperRequest) ([]byte, error) {
	if adapter.run != nil {
		return adapter.run(ctx, request)
	}
	if adapter.NodeCommand == "" || adapter.HelperPath == "" {
		return nil, errors.New("Claude helper is not configured")
	}
	content, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, adapter.NodeCommand, adapter.HelperPath)
	command.Dir = request.CWD
	if request.Action == "observe" {
		command.Dir = adapter.ConfigRoot
	}
	command.Env = replaceEnvironment(os.Environ(), "CLAUDE_CONFIG_DIR", adapter.ConfigRoot)
	if adapter.SDKEntry != "" {
		command.Env = replaceEnvironment(command.Env, "COCONET_CLAUDE_SDK_ENTRY", adapter.SDKEntry)
	}
	command.Stdin = bytes.NewReader(content)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &limitedWriter{destination: &stderr, remaining: 4000}
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("Claude helper interrupted: %w", ctx.Err())
		}
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, exec.ErrNotFound) {
			return nil, errors.New("Claude helper could not start: required Node runtime or working directory is missing; check the Runtime bundle and Agent profile")
		}
		var failure struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(stdout.Bytes(), &failure) == nil {
			switch failure.Error.Code {
			case "native_session_missing":
				return nil, adapters.ErrNativeSessionMissing
			case "native_session_ambiguous":
				return nil, adapters.ErrNativeSessionAmbiguous
			case "native_session_unreadable":
				return nil, adapters.ErrNativeSessionUnreadable
			}
		}
		detail := sanitize(stderr.String(), request.CWD)
		if strings.TrimSpace(detail) == "" {
			return nil, errors.New("Claude helper process failed without diagnostic output; check its executable and Runtime bundle")
		}
		return nil, fmt.Errorf("Claude helper failed: %s", detail)
	}
	return stdout.Bytes(), nil
}

func defaultConfigRoot() string {
	if root := os.Getenv("CLAUDE_CONFIG_DIR"); root != "" {
		return root
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude"
	}
	return filepath.Join(home, ".claude")
}

func defaultHelperPath() string {
	if path := os.Getenv("COCONET_CLAUDE_HELPER"); path != "" {
		return path
	}
	if executable, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(executable), "lib", "claude", "helper.mjs")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate
		}
	}
	_, source, _, ok := runtime.Caller(0)
	if ok {
		return filepath.Join(filepath.Dir(source), "helper", "helper.mjs")
	}
	return ""
}

func defaultNodeCommand() string {
	if command := os.Getenv("COCONET_CLAUDE_NODE"); command != "" {
		return command
	}
	if candidate := bundledPath("node", "node"); candidate != "" {
		return candidate
	}
	return "node"
}

func defaultSDKEntry() string {
	if path := os.Getenv("COCONET_CLAUDE_SDK_ENTRY"); path != "" {
		return path
	}
	return bundledPath("claude", "sdk.mjs")
}

func bundledPath(parts ...string) string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	candidate := filepath.Join(append([]string{filepath.Dir(executable), "lib"}, parts...)...)
	if _, err := os.Stat(candidate); err != nil {
		return ""
	}
	return candidate
}

func environmentOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func replaceEnvironment(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func sanitize(message, cwd string) string {
	message = strings.ReplaceAll(message, cwd, "<cwd>")
	if home, err := os.UserHomeDir(); err == nil {
		message = strings.ReplaceAll(message, home, "<user-home>")
	}
	return strings.TrimSpace(message)
}

type limitedWriter struct {
	destination *bytes.Buffer
	remaining   int
}

func (writer *limitedWriter) Write(content []byte) (int, error) {
	original := len(content)
	if writer.remaining > 0 {
		if len(content) > writer.remaining {
			content = content[:writer.remaining]
		}
		writer.remaining -= len(content)
		_, _ = writer.destination.Write(content)
	}
	return original, nil
}
