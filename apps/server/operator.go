package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xfey/coconet/packages/clihelp"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/observability"
	"github.com/xfey/coconet/packages/projectlink"
	bolt "go.etcd.io/bbolt"
)

const (
	legacyServerConfigSchemaVersion   = 1
	artifactServerConfigSchemaVersion = 2
	serverConfigSchemaVersion         = 3
)

type serverConfig struct {
	SchemaVersion           int    `json:"schema_version"`
	Listen                  string `json:"listen"`
	AdvertisedEndpoint      string `json:"advertised_endpoint,omitempty"`
	MetadataDriver          string `json:"metadata_driver"`
	DataPath                string `json:"data_path"`
	IdentityPath            string `json:"identity_path"`
	RegistrationTokenSHA256 string `json:"registration_token_sha256,omitempty"`
	ArtifactDriver          string `json:"artifact_driver,omitempty"`
	ArtifactPath            string `json:"artifact_path,omitempty"`
	RequireArtifactStore    bool   `json:"require_artifact_store,omitempty"`
}

func runServerCLI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printServerUsage(stderr)
		return 2
	}
	if handled, code := clihelp.Handle(clihelp.Server(), args, stdout, stderr); handled {
		return code
	}
	started := time.Now()
	command := args[0]
	code := 0
	switch args[0] {
	case "version", "--version", "-v":
		if len(args) != 1 {
			printServerUsage(stderr)
			return 2
		}
		fmt.Fprintf(stdout, "coconet-server %s\n", version)
		return 0
	case "init":
		code = runServerInit(args[1:], stdout, stderr)
	case "account":
		code = runServerAccount(ctx, args[1:], stdout, stderr)
	case "serve":
		return runServerServe(ctx, args[1:], stdout, stderr)
	case "status":
		code = runServerStatus(ctx, args[1:], stdout, stderr)
	case "backup":
		code = runServerBackup(args[1:], stdout, stderr)
	case "compact":
		code = runSnapshotCompact(args[1:], stdout, stderr)
	case "restore":
		code = runServerRestore(args[1:], stdout, stderr)
	default:
		printServerUsage(stderr)
		return 2
	}
	recordOperatorResult(ctx, stderr, command, code, time.Since(started))
	return code
}

func recordOperatorResult(ctx context.Context, output io.Writer, command string, code int, duration time.Duration) {
	event := observability.Event{
		Severity: observability.SeverityInfo, Operation: "operator." + command,
		Status: contracts.StatusOK, DurationMS: duration.Milliseconds(),
	}
	if code == 2 {
		event.Severity = observability.SeverityWarning
		event.Status = contracts.StatusInvalidRequest
		event.ErrorCode = "operator_invalid_arguments"
	} else if code != 0 {
		event.Severity = observability.SeverityError
		event.Status = contracts.StatusInternalError
		event.FailureCategory = contracts.FailureServerInternal
		event.ErrorCode = "operator_failed"
	}
	_ = observability.NewJSONLogger(output, "server", version).Record(ctx, event)
}

func printServerUsage(output io.Writer) {
	root := clihelp.Server()
	clihelp.Write(output, root, root.Name, nil)
}

func runServerInit(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dataDir := flags.String("data-dir", "", "persistent operator directory")
	listen := flags.String("listen", "127.0.0.1:7788", "HTTP listen address")
	advertisedEndpoint := flags.String("advertised-endpoint", "", "canonical client-facing Server origin")
	artifactDriver := flags.String("artifact-store", "filesystem", "artifact storage driver: filesystem or s3")
	configuredArtifactPath := flags.String("artifact-path", "", "absolute filesystem artifact directory")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || strings.TrimSpace(*dataDir) == "" || strings.TrimSpace(*advertisedEndpoint) == "" {
		fmt.Fprintln(stderr, "usage: coconet-server init --data-dir /var/lib/coconet --advertised-endpoint https://coconet.example.com [--listen 127.0.0.1:7788] [--artifact-store filesystem|s3] [--artifact-path /data/coconet-artifacts]")
		return 2
	}
	selectedArtifactDriver := strings.ToLower(strings.TrimSpace(*artifactDriver))
	if selectedArtifactDriver != "filesystem" && selectedArtifactDriver != "s3" {
		fmt.Fprintln(stderr, "--artifact-store must be filesystem or s3")
		return 2
	}
	if selectedArtifactDriver == "s3" && strings.TrimSpace(*configuredArtifactPath) != "" {
		fmt.Fprintln(stderr, "--artifact-path can only be used with --artifact-store filesystem")
		return 2
	}
	canonicalEndpoint, err := projectlink.ParseServerOrigin(strings.TrimSpace(*advertisedEndpoint), true)
	if err != nil {
		fmt.Fprintf(stderr, "invalid --advertised-endpoint: %v\n", err)
		return 2
	}
	absolute, err := filepath.Abs(*dataDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	absolute = filepath.Clean(absolute)
	configPath := filepath.Join(absolute, "server.json")
	identityPath := filepath.Join(absolute, "deployment.json")
	dataPath := filepath.Join(absolute, "server.db")
	artifactPath := ""
	if selectedArtifactDriver == "filesystem" {
		artifactPath = strings.TrimSpace(*configuredArtifactPath)
		if artifactPath == "" {
			artifactPath = filepath.Join(absolute, "artifacts")
		} else if !filepath.IsAbs(artifactPath) {
			fmt.Fprintln(stderr, "--artifact-path must be absolute")
			return 2
		} else {
			artifactPath = filepath.Clean(artifactPath)
		}
	}
	if _, err := os.Stat(configPath); err == nil {
		fmt.Fprintf(stderr, "%s already exists; refusing to replace an initialized Deployment\n", configPath)
		return 1
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stderr, err)
		return 1
	}
	config := serverConfig{
		SchemaVersion: serverConfigSchemaVersion, Listen: strings.TrimSpace(*listen), AdvertisedEndpoint: canonicalEndpoint, MetadataDriver: "bbolt",
		DataPath: dataPath, IdentityPath: identityPath,
		ArtifactDriver: selectedArtifactDriver, ArtifactPath: artifactPath,
	}
	if err := validateServerConfig(config); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	identity, err := loadOrCreateDeploymentIdentity(identityPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	storage, err := openStore(dataPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := storage.bindDeployment(identity.deploymentID); err != nil {
		_ = storage.close()
		fmt.Fprintln(stderr, err)
		return 1
	}
	if config.ArtifactDriver == "filesystem" {
		artifacts, err := openFilesystemArtifactStore(config.ArtifactPath)
		if err != nil {
			_ = storage.close()
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := storage.bindArtifactBackend(artifacts.BackendIdentity()); err != nil {
			_ = storage.close()
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if err := storage.close(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := atomicWriteServerJSON(configPath, config, 0o600); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Coconet Deployment initialized.\nDeployment ID: %s\nConfig: %s\nAdvertised endpoint: %s\n", identity.deploymentID, configPath, config.AdvertisedEndpoint)
	return 0
}

func runServerServe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	started := time.Now()
	logger := observability.NewJSONLogger(stderr, "server", version)
	configPath, code := parseConfigFlag("serve", args, stderr)
	if code != 0 {
		recordOperatorResult(ctx, stderr, "serve", code, time.Since(started))
		return code
	}
	preflightConfig, err := loadServerConfig(configPath)
	if err != nil {
		recordServerFailure(ctx, logger, "server.open", err, time.Since(started))
		return 1
	}
	if preflightConfig.AdvertisedEndpoint == "" {
		recordServerFailure(ctx, logger, "server.open", errors.New("legacy Server config is missing advertised_endpoint; migrate it to schema 3 before serving"), time.Since(started))
		return 1
	}
	addressResolver, err := parseTrustedProxyCIDRs(os.Getenv("COCONET_TRUSTED_PROXY_CIDRS"))
	if err != nil {
		recordServerFailure(ctx, logger, "server.open", err, time.Since(started))
		return 1
	}
	config, identity, storage, artifacts, err := openConfiguredServer(ctx, configPath)
	if err != nil {
		recordServerFailure(ctx, logger, "server.open", err, time.Since(started))
		return 1
	}
	defer storage.close()
	authority := newAuthority(storage, artifacts)
	webOrigin, err := webOriginFromEnvironment(config.AdvertisedEndpoint)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	login, err := loginProviderFromEnvironment(ctx, webOrigin)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	handler := newHandlerWithLogger(version, identity, authority, config.AdvertisedEndpoint, addressResolver, logger).(*serverHandler)
	if err := authority.revokeLegacyTestAccess(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := storage.configureCapacity(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if _, err := storage.relayStore(); err != nil {
		recordServerFailure(ctx, logger, "server.open", err, time.Since(started))
		return 1
	}
	if os.Getenv("COCONET_OPERATOR_SOCKET") != "" {
		stopOperator, err := startOperatorSocket(ctx, config, storage)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		defer stopOperator()
	}
	handler.auth = login
	// Only pin the browser host when explicitly configured; single-origin and
	// loopback deployments retain their existing routing behavior.
	if strings.TrimSpace(os.Getenv("COCONET_WEB_ORIGIN")) != "" {
		handler.webOrigin = webOrigin
	}
	server := &http.Server{
		Addr: config.Listen, Handler: handler,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: 5 * time.Second,
	}
	shutdownDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
		case <-shutdownDone:
		}
	}()
	_ = logger.Record(ctx, observability.Event{
		Severity: observability.SeverityInfo, Operation: "server.start", Status: contracts.StatusOK,
		DurationMS: time.Since(started).Milliseconds(),
	})
	fmt.Fprintf(stdout, "coconet-server %s serving Deployment %s on %s\n", version, identity.deploymentID, config.Listen)
	err = server.ListenAndServe()
	close(shutdownDone)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		recordServerFailure(ctx, logger, "server.serve", err, time.Since(started))
		return 1
	}
	_ = logger.Record(context.Background(), observability.Event{
		Severity: observability.SeverityInfo, Operation: "server.stop", Status: contracts.StatusOK,
		DurationMS: time.Since(started).Milliseconds(),
	})
	return 0
}

func recordServerFailure(ctx context.Context, logger *observability.JSONLogger, operation string, err error, duration time.Duration) {
	failure := observability.Describe(err)
	_ = logger.Record(ctx, observability.Event{
		Severity: observability.SeverityError, Operation: operation,
		Status: observability.StatusForFailure(failure.Category), FailureCategory: failure.Category,
		ErrorCode: failure.Code, Retryable: failure.Retryable, DurationMS: duration.Milliseconds(),
	})
}

func runServerBackup(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "Server config path")
	outputPath := flags.String("output", "", "new backup file")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *configPath == "" || *outputPath == "" {
		fmt.Fprintln(stderr, "usage: coconet-server backup --config /var/lib/coconet/server.json --output /safe/path/server.db")
		return 2
	}
	config, err := loadServerConfig(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if config.MetadataDriver != "bbolt" {
		fmt.Fprintln(stderr, "operator backup currently supports only bbolt; use PostgreSQL-native backup tooling")
		return 1
	}
	objects, err := configuredArtifactStore(context.Background(), config)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := backupComplete(context.Background(), config, objects, *outputPath); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Complete backup written to %s (identity, metadata, conversation and native objects, including initialized Relay storage)\n", *outputPath)
	return 0
}

func runServerRestore(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "Server config path")
	inputPath := flags.String("input", "", "validated bbolt backup")
	dataDir := flags.String("data-dir", "", "new empty destination for a complete Deployment restore")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || (*configPath == "") == (*dataDir == "") || *inputPath == "" {
		fmt.Fprintln(stderr, "usage: coconet-server restore --input /safe/path/server.db --data-dir /new/deployment (or --config for legacy metadata-only deployments)")
		return 2
	}
	if *dataDir != "" {
		path, err := restoreDeployment(context.Background(), *inputPath, *dataDir)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "Complete Deployment restored and verified. Identity and account mappings preserved. Start with --config %s; keep the previous data directory for rollback.\n", path)
		return 0
	}
	config, err := loadServerConfig(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if config.MetadataDriver != "bbolt" {
		fmt.Fprintln(stderr, "operator restore currently supports only bbolt; use PostgreSQL-native restore tooling")
		return 1
	}
	if err := requireLegacyRestore(config, *inputPath); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	identity, err := loadExistingDeploymentIdentity(config.IdentityPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	objects, err := configuredArtifactStore(context.Background(), config)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	rollback, err := restoreBbolt(config.DataPath, *inputPath, identity.deploymentID, func() error {
		return restoreCompleteObjects(context.Background(), *inputPath, identity.deploymentID, objects)
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "Complete restore verified metadata and content objects.")
	if rollback != "" {
		fmt.Fprintf(stdout, "Previous database retained at %s\n", rollback)
	}
	return 0
}

func parseConfigFlag(command string, args []string, stderr io.Writer) (string, int) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "Server config path")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *configPath == "" {
		fmt.Fprintf(stderr, "usage: coconet-server %s --config /var/lib/coconet/server.json\n", command)
		return "", 2
	}
	return *configPath, 0
}

func loadServerConfig(path string) (serverConfig, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return serverConfig{}, fmt.Errorf("read Server config: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return serverConfig{}, fmt.Errorf("secure Server config: %w", err)
	}
	return decodeServerConfig(content)
}

func loadServerConfigReadOnly(path string) (serverConfig, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return serverConfig{}, fmt.Errorf("read Server config: %w", err)
	}
	return decodeServerConfig(content)
}

func decodeServerConfig(content []byte) (serverConfig, error) {
	var config serverConfig
	if err := decodeStrictJSON(content, &config); err != nil {
		return serverConfig{}, fmt.Errorf("decode Server config: %w", err)
	}
	if config.SchemaVersion == serverConfigSchemaVersion {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(content, &fields); err != nil {
			return serverConfig{}, fmt.Errorf("decode Server config fields: %w", err)
		}
		for _, legacyField := range []string{"registration_token_sha256", "require_artifact_store"} {
			if _, exists := fields[legacyField]; exists {
				return serverConfig{}, fmt.Errorf("Server config schema 3 cannot contain legacy field %q", legacyField)
			}
		}
	}
	if err := validateServerConfig(config); err != nil {
		return serverConfig{}, err
	}
	return config, nil
}

func validateServerConfig(config serverConfig) error {
	if (config.SchemaVersion != legacyServerConfigSchemaVersion && config.SchemaVersion != artifactServerConfigSchemaVersion && config.SchemaVersion != serverConfigSchemaVersion) || strings.TrimSpace(config.Listen) == "" || (config.MetadataDriver != "bbolt" && config.MetadataDriver != "postgres") || !filepath.IsAbs(config.IdentityPath) {
		return errors.New("Server config is incomplete or unsupported")
	}
	if _, _, err := net.SplitHostPort(config.Listen); err != nil {
		return errors.New("Server listen address must use host:port form")
	}
	if config.MetadataDriver == "bbolt" && !filepath.IsAbs(config.DataPath) {
		return errors.New("bbolt data_path must be absolute")
	}
	if config.MetadataDriver == "bbolt" && filepath.Clean(config.DataPath) == filepath.Clean(config.IdentityPath) {
		return errors.New("bbolt data_path and identity_path must differ")
	}
	if config.AdvertisedEndpoint != "" {
		canonical, err := projectlink.ParseServerOrigin(config.AdvertisedEndpoint, true)
		if err != nil || canonical != config.AdvertisedEndpoint {
			return errors.New("advertised_endpoint must be a canonical supported Server origin")
		}
	}
	if config.SchemaVersion == serverConfigSchemaVersion {
		if config.AdvertisedEndpoint == "" || config.RegistrationTokenSHA256 != "" {
			return errors.New("Server config schema 3 requires advertised_endpoint and cannot contain registration credentials")
		}
	} else {
		registrationHash, hashErr := hex.DecodeString(config.RegistrationTokenSHA256)
		if hashErr != nil || len(registrationHash) != 32 {
			return errors.New("legacy Server config registration token hash is invalid")
		}
	}
	if config.SchemaVersion == legacyServerConfigSchemaVersion {
		if config.ArtifactDriver != "" || config.ArtifactPath != "" {
			return errors.New("Server config schema 1 cannot declare artifact_driver or artifact_path")
		}
		return nil
	}
	if config.RequireArtifactStore || (config.ArtifactDriver != "filesystem" && config.ArtifactDriver != "s3") {
		return errors.New("Server config schema 2 or 3 requires artifact_driver filesystem or s3")
	}
	if config.ArtifactDriver == "filesystem" {
		clean := filepath.Clean(config.ArtifactPath)
		volumeRoot := filepath.VolumeName(clean) + string(filepath.Separator)
		if !filepath.IsAbs(config.ArtifactPath) || clean == volumeRoot {
			return errors.New("filesystem artifact_path must be an absolute dedicated directory")
		}
		if clean == filepath.Clean(config.DataPath) || clean == filepath.Clean(config.IdentityPath) {
			return errors.New("artifact_path must differ from metadata and Deployment identity paths")
		}
	} else if config.ArtifactPath != "" {
		return errors.New("s3 artifact storage must not declare artifact_path")
	}
	return nil
}

func openConfiguredServer(ctx context.Context, configPath string) (serverConfig, *deploymentIdentity, *store, artifactStore, error) {
	config, err := loadServerConfig(configPath)
	if err != nil {
		return serverConfig{}, nil, nil, nil, err
	}
	identity, err := loadExistingDeploymentIdentity(config.IdentityPath)
	if err != nil {
		return serverConfig{}, nil, nil, nil, err
	}
	artifacts, err := configuredArtifactStore(ctx, config)
	if err != nil {
		return serverConfig{}, nil, nil, nil, err
	}
	var storage *store
	if config.MetadataDriver == "postgres" {
		storage, err = openPostgresStore(os.Getenv("COCONET_DATABASE_URL"), artifacts)
	} else {
		storage, err = openStore(config.DataPath, artifacts)
	}
	if err != nil {
		return serverConfig{}, nil, nil, nil, err
	}
	if err := storage.bindDeployment(identity.deploymentID); err != nil {
		_ = storage.close()
		return serverConfig{}, nil, nil, nil, err
	}
	if artifacts != nil {
		if err := bindConfiguredArtifactBackend(storage, artifacts); err != nil {
			_ = storage.close()
			return serverConfig{}, nil, nil, nil, err
		}
	}
	return config, identity, storage, artifacts, nil
}

func bindConfiguredArtifactBackend(storage *store, artifacts artifactStore) error {
	if artifacts.Driver() == "filesystem" {
		bound, err := storage.artifactBackendID()
		if err != nil {
			return err
		}
		if bound == "" {
			hasReferences := false
			if err := storage.view(func(state databaseState) error {
				hasReferences = state.Chunks.Len() > 0 || state.Snapshots.Len() > 0
				return nil
			}); err != nil {
				return err
			}
			if hasReferences {
				return errors.New("legacy metadata contains artifact references but no backend identity; open it with the original S3 backend before migrating to filesystem")
			}
		}
	}
	return storage.bindArtifactBackend(artifacts.BackendIdentity())
}

func configuredArtifactStore(ctx context.Context, config serverConfig) (artifactStore, error) {
	if config.SchemaVersion >= artifactServerConfigSchemaVersion && config.ArtifactDriver == "filesystem" {
		store, err := openFilesystemArtifactStore(config.ArtifactPath)
		if err != nil {
			return nil, observability.Wrap(err, contracts.FailureObjectStore, "filesystem_store_open", false)
		}
		return store, nil
	}
	required := (config.SchemaVersion >= artifactServerConfigSchemaVersion && config.ArtifactDriver == "s3") || (config.SchemaVersion == legacyServerConfigSchemaVersion && config.RequireArtifactStore)
	endpoint := os.Getenv("COCONET_S3_ENDPOINT")
	if endpoint == "" {
		if required {
			return nil, observability.Wrap(errors.New("COCONET_S3_ENDPOINT is required by Server config"), contracts.FailureObjectStore, "object_store_config_missing", false)
		}
		return nil, nil
	}
	for _, name := range []string{"COCONET_S3_BUCKET", "COCONET_S3_ACCESS_KEY", "COCONET_S3_SECRET_KEY"} {
		if os.Getenv(name) == "" {
			return nil, observability.Wrap(fmt.Errorf("%s is required when S3 artifact storage is configured", name), contracts.FailureObjectStore, "object_store_config_missing", false)
		}
	}
	store, err := openS3ArtifactStore(ctx, s3Config{
		Endpoint: endpoint, Region: environmentOr("COCONET_S3_REGION", "us-east-1"), Bucket: os.Getenv("COCONET_S3_BUCKET"),
		AccessKey: os.Getenv("COCONET_S3_ACCESS_KEY"), SecretKey: os.Getenv("COCONET_S3_SECRET_KEY"),
	})
	if err != nil {
		return nil, observability.Wrap(err, contracts.FailureObjectStore, "object_store_open", true)
	}
	return store, nil
}

func artifactBackupNotice(config serverConfig) string {
	if config.SchemaVersion >= artifactServerConfigSchemaVersion && config.ArtifactDriver == "filesystem" {
		return "Artifact data is not included; separately back up " + config.ArtifactPath + " together with deployment.json."
	}
	return "Artifact data is not included; separately protect the configured S3-compatible bucket and deployment.json."
}

func backupBbolt(dataPath, outputPath string) error {
	absoluteData, err := filepath.Abs(dataPath)
	if err != nil {
		return err
	}
	absoluteOutput, err := filepath.Abs(outputPath)
	if err != nil {
		return err
	}
	if filepath.Clean(absoluteData) == filepath.Clean(absoluteOutput) {
		return errors.New("backup output must differ from the live database")
	}
	if info, err := os.Lstat(absoluteData); err != nil {
		return fmt.Errorf("inspect live bbolt database: %w", err)
	} else if !info.Mode().IsRegular() {
		return errors.New("live bbolt database must be a regular file")
	}
	if _, err := os.Stat(absoluteOutput); err == nil {
		return errors.New("backup output already exists; refusing to overwrite it")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absoluteOutput), 0o700); err != nil {
		return err
	}
	// A pre-upgrade backup must preserve the exact old schema. Opening through
	// openStore would migrate the live file before the backup was taken.
	database, err := bolt.Open(absoluteData, 0o400, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return fmt.Errorf("open bbolt database for backup (stop coconet-server first): %w", err)
	}
	defer database.Close()
	var deploymentID string
	if err := database.View(func(tx *bolt.Tx) error {
		if meta := tx.Bucket(metaBucket); meta != nil {
			deploymentID = string(meta.Get(deploymentKey))
		}
		return nil
	}); err != nil {
		return err
	}
	if err := validateBboltDatabase(database, deploymentID); err != nil {
		return err
	}
	if err := database.View(func(tx *bolt.Tx) error { return tx.CopyFile(absoluteOutput, 0o600) }); err != nil {
		_ = os.Remove(absoluteOutput)
		return fmt.Errorf("copy bbolt backup: %w", err)
	}
	return os.Chmod(absoluteOutput, 0o600)
}

func restoreBbolt(dataPath, inputPath, expectedDeploymentID string, beforeAdopt ...func() error) (string, error) {
	absoluteData, err := filepath.Abs(dataPath)
	if err != nil {
		return "", err
	}
	absoluteInput, err := filepath.Abs(inputPath)
	if err != nil {
		return "", err
	}
	if filepath.Clean(absoluteData) == filepath.Clean(absoluteInput) {
		return "", errors.New("restore input must differ from the live database")
	}
	if info, err := os.Lstat(absoluteInput); err != nil {
		return "", fmt.Errorf("inspect bbolt backup: %w", err)
	} else if !info.Mode().IsRegular() {
		return "", errors.New("bbolt backup must be a regular file")
	}
	liveExists := true
	if info, err := os.Lstat(absoluteData); errors.Is(err, os.ErrNotExist) {
		liveExists = false
	} else if err != nil {
		return "", fmt.Errorf("inspect live bbolt database: %w", err)
	} else if !info.Mode().IsRegular() {
		return "", errors.New("live bbolt database must be a regular file")
	}
	if err := validateBboltSnapshotReadOnly(absoluteInput, expectedDeploymentID); err != nil {
		return "", fmt.Errorf("validate bbolt backup: %w", err)
	}
	if liveExists {
		live, err := bolt.Open(absoluteData, 0o600, &bolt.Options{Timeout: time.Second})
		if err != nil {
			return "", fmt.Errorf("lock live bbolt database (stop coconet-server first): %w", err)
		}
		defer live.Close()
		if err := validateBboltDatabase(live, expectedDeploymentID); err != nil {
			return "", fmt.Errorf("validate live bbolt database: %w", err)
		}
	}
	input, err := os.Open(absoluteInput)
	if err != nil {
		return "", err
	}
	defer input.Close()
	temporary, err := os.CreateTemp(filepath.Dir(absoluteData), ".restore-*.db")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return "", err
	}
	if _, err := io.Copy(temporary, input); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := validateBboltSnapshotReadOnly(temporaryPath, expectedDeploymentID); err != nil {
		return "", fmt.Errorf("validate staged restore: %w", err)
	}
	if liveExists {
		if err := preserveServerFileOwnership(absoluteData, temporaryPath); err != nil {
			return "", err
		}
	}
	// Keep both old and replacement inodes locked across atomic adoption.
	staged, err := bolt.Open(temporaryPath, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return "", err
	}
	defer staged.Close()
	for _, prepare := range beforeAdopt {
		if err := prepare(); err != nil {
			return "", err
		}
	}
	rollback := ""
	if liveExists {
		rollback = absoluteData + ".before-restore-" + time.Now().UTC().Format("20060102T150405Z")
		// A hard link preserves rollback without opening a gap at the live path.
		if err := os.Link(absoluteData, rollback); err != nil {
			return "", err
		}
		if err := os.Rename(temporaryPath, absoluteData); err != nil {
			return rollback, err
		}
	} else {
		// Never replace a database concurrently created after the initial check.
		if err := os.Link(temporaryPath, absoluteData); err != nil {
			return "", err
		}
	}
	directory, err := os.Open(filepath.Dir(absoluteData))
	if err != nil {
		return rollback, err
	}
	if err := errors.Join(directory.Sync(), directory.Close()); err != nil {
		return rollback, err
	}
	return rollback, nil
}

func validateBboltSnapshotReadOnly(path, expectedDeploymentID string) error {
	database, err := bolt.Open(path, 0o400, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return err
	}
	defer database.Close()
	return validateBboltDatabase(database, expectedDeploymentID)
}

func validateBboltDatabase(database *bolt.DB, expectedDeploymentID string) error {
	return database.View(func(tx *bolt.Tx) error {
		meta := tx.Bucket(metaBucket)
		state := tx.Bucket(stateBucket)
		if meta == nil || state == nil {
			return errors.New("bbolt backup is missing Coconet metadata buckets")
		}
		version := meta.Get(schemaKey)
		if len(version) != 1 || version[0] < 1 || version[0] > databaseSchemaVersion {
			return fmt.Errorf("unsupported database schema version %v", version)
		}
		if string(meta.Get(deploymentKey)) != expectedDeploymentID {
			return errors.New("bbolt backup belongs to a different or unbound Coconet Deployment")
		}
		if int(version[0]) == databaseSchemaVersion {
			return validateRecordSnapshot(tx)
		}
		if int(version[0]) >= 23 {
			content, err := exportRecordJSON(tx)
			if err != nil {
				return err
			}
			_, err = decodeStateJSON(content)
			return err
		}
		content := state.Get(stateKey)
		if content == nil {
			return errors.New("database state is missing")
		}
		_, err := decodeStateJSON(content)
		return err
	})
}
