package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/xfey/coconet/packages/subprocess"
)

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > b.limit-b.Len() {
		p = p[:b.limit-b.Len()]
		b.overflow = true
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

func (r Local) claude(ctx context.Context, work string, request Request) (Response, error) {
	// SIMPLE plus a fresh cwd removes implicit project discovery;
	// MCP, skills, hooks and built-in tools are independently
	// disabled. No --dangerously-skip-permissions is needed.
	args := []string{"-p", "--output-format", "json",
		"--system-prompt", request.System, "--json-schema", string(request.Schema),
		"--tools", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`,
		"--setting-sources", "user", "--settings", `{"disableAllHooks":true}`,
		"--disable-slash-commands", "--no-session-persistence", "--permission-mode", "dontAsk"}
	if r.Config.Model != "" {
		args = append(args, "--model", r.Config.Model)
	}
	cmd := r.command(work, args...)
	cmd.Stdin = strings.NewReader(string(request.Input))
	out, diagnostic := &boundedBuffer{limit: MaxResponse}, &boundedBuffer{limit: 32 << 10}
	cmd.Stdout, cmd.Stderr = out, diagnostic
	err := subprocess.Run(ctx, cmd)
	response := Response{Visible: out.String(), Model: r.Config.Model}
	if out.overflow {
		return response, errors.New("provider response exceeds archive limit")
	}
	result, parseErr := decodeClaudeResult(out.Bytes())
	if parseErr == nil {
		response.Usage = result.Usage
		models := make([]string, 0, len(result.ModelUsage))
		for model := range result.ModelUsage {
			models = append(models, model)
		}
		sort.Strings(models)
		if len(models) > 0 {
			response.Model = strings.Join(models, ", ")
		}
	}
	if err != nil {
		// Provider text and stderr may contain credentials. Only a fixed
		// classification enters ordinary logs; the private receipt keeps detail.
		classification := claudeFailure(result.Result + "\n" + diagnostic.String())
		return response, providerFailure(classification, fmt.Errorf("claude process failed (%s): %w", classification, err))
	}
	if parseErr != nil {
		return response, errors.New("claude returned an invalid result envelope")
	}
	if result.Type != "result" || result.IsError || result.Subtype != "success" {
		classification := claudeFailure(result.Result)
		return response, providerFailure(classification, fmt.Errorf("claude did not complete inference successfully (%s)", classification))
	}
	response.Text = result.Result
	if len(result.Structured) > 0 && string(result.Structured) != "null" {
		response.Text = string(result.Structured)
	}
	if strings.TrimSpace(response.Text) == "" {
		return response, errors.New("claude returned no final output")
	}
	return response, nil
}

type claudeResult struct {
	ModelUsage map[string]json.RawMessage `json:"modelUsage"`
	Type       string                     `json:"type"`
	Subtype    string                     `json:"subtype"`
	IsError    bool                       `json:"is_error"`
	Result     string                     `json:"result"`
	Structured json.RawMessage            `json:"structured_output"`
	Usage      json.RawMessage            `json:"usage"`
}

// Wrappers may print authentication/status lines before the native JSON
// receipt. Accept that prelude, but require exactly one final JSON document.
// Never select the last of multiple structured responses or ignore a suffix.
func decodeClaudeResult(output []byte) (claudeResult, error) {
	var result claudeResult
	for len(output) > 0 {
		output = bytes.TrimSpace(output)
		if len(output) == 0 {
			break
		}
		if output[0] == '{' || output[0] == '[' {
			err := json.Unmarshal(output, &result)
			if err == nil && result.Type != "result" {
				err = errors.New("unexpected result type")
			}
			return result, err
		}
		_, remaining, found := bytes.Cut(output, []byte("\n"))
		if !found {
			break
		}
		output = remaining
	}
	return result, errors.New("missing result envelope")
}

func claudeFailure(message string) string {
	message = strings.ToLower(message)
	switch {
	case strings.Contains(message, "context length") || strings.Contains(message, "context window") || strings.Contains(message, "prompt is too long"):
		return "context_capacity_exceeded: the source model cannot accept the complete request; no content was truncated"
	case strings.Contains(message, "connection reset") || strings.Contains(message, "connection refused") || strings.Contains(message, "network error") || strings.Contains(message, "503") || strings.Contains(message, "502"):
		return "transient_network: provider connection is temporarily unavailable; Coconet will retry automatically"
	case strings.Contains(message, "thinking") && (strings.Contains(message, "not supported") || strings.Contains(message, "unsupported")):
		return "thinking_configuration_rejected"
	case strings.Contains(message, "401") || strings.Contains(message, "authentication"):
		return "authentication_required: sign in again with the same Claude installation and profile used by the source conversation, then retry paused Coconet tasks"
	case strings.Contains(message, "429") || strings.Contains(message, "rate limit"):
		return "rate_limited: wait for the provider limit to clear; inspect Coconet task status for the next retry or a paused task"
	default:
		return "provider_error"
	}
}
