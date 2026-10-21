package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/xfey/coconet/packages/clihelp"
	"github.com/xfey/coconet/packages/contracts"
)

var version = "dev"

func main() {
	ctx := context.Background()
	cancel := func() {}
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "update" || args[0] == "_install-guard" || args[0] == "_resume-after-update" || args[0] == "agent" || args[0] == "_agent-setup" || args[0] == "_agent-sync" || args[0] == "_local-daemon" || args[0] == "local-dag" || args[0] == "inference" || shouldAutoSyncAgentIntegrations(args)) {
		ctx, cancel = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	}
	code := run(ctx, args, os.Stdin, os.Stdout, os.Stderr)
	cancel()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeCLIHelp(stderr)
		return 2
	}
	if handled, code := clihelp.Handle(clihelp.Runtime(), args, stdout, stderr); handled {
		return code
	}
	defer maybeNotifyUpdate(args, stderr)
	if shouldAutoSyncAgentIntegrations(args) {
		autoSyncRegisteredAgentIntegrations(ctx, stderr)
		if ctx.Err() != nil {
			fmt.Fprintln(stderr, "Agent setup cancelled.")
			return 1
		}
	}
	switch args[0] {
	case "_update-check":
		return runUpdateCheck(ctx)
	case "update":
		return runUpdate(ctx, args[1:], stdout, stderr)
	case "_install-guard":
		return runInstallGuard(ctx, args[1:], stdin, stdout, stderr)
	case "_resume-after-update":
		return runResumeAfterUpdate(ctx, args[1:], stderr)
	case "server":
		return runServerCommand(args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "coconet %s\n", version)
		return 0
	case "health":
		return runHealth(ctx, args[1:], stdout, stderr)
	case "inference":
		return runInferenceCommandWithInput(ctx, args[1:], stdin, stdout, stderr)
	case "history":
		return runHistoryCommand(ctx, args[1:], stdout, stderr)
	case "diagnostics":
		return runDiagnostics(ctx, args[1:], stdout, stderr)
	case "storage":
		return runStorage(ctx, args[1:], stdout, stderr)
	case "local-dag":
		return runLocalDAG(ctx, args[1:], stdout, stderr)
	case "_local-daemon":
		return runManagedLocalDAG(ctx, args[1:], stdout, stderr)
	case "login", "logout", "whoami":
		return runLoginCommand(ctx, args[0], args[1:], stdout, stderr)
	case "init":
		return runInit(ctx, args[1:], stdin, stdout, stderr)
	case "connect":
		return runConnect(ctx, args[1:], stdin, stdout, stderr)
	case "status":
		return runProjectStatus(ctx, args[1:], stdout, stderr)
	case "disconnect":
		return runDisconnect(ctx, args[1:], stdout, stderr)
	case "agent":
		return runAgent(ctx, args[1:], stdout, stderr)
	case "account":
		return runAccount(ctx, args[1:], stdout, stderr)
	case "session":
		return runSessionCommand(ctx, args[1:], stdout, stderr)
	case "library":
		return runLibraryCommand(ctx, args[1:], stdout, stderr, nil)
	case "fork":
		return runNativeForkCommand(ctx, args[1:], stdout, stderr)
	case "projection":
		return runProjectionCommand(ctx, args[1:], stdout, stderr)
	case "uninstall":
		return runUninstall(ctx, args[1:], stdout, stderr)
	case "manage":
		fmt.Fprintln(stderr, "`coconet manage` was removed. Use `coconet status` to view your project or `coconet disconnect` to leave it.")
		return 2
	case "hook":
		return runHook(args[1:], stdin, stdout, stderr)
	case "_agent-sync":
		paths, err := resolveRuntimePaths()
		if err != nil {
			return 1
		}
		if err := syncRegisteredAgentIntegrations(ctx, paths, true); err != nil {
			return 1
		}
		return 0
	case "_agent-setup":
		return runAgentSetup(ctx, args[1:], stdout, stderr)
	case "_sync":
		return runSync(ctx, args[1:])
	case "mcp":
		targetAgent := contracts.AgentType("")
		if len(args) == 3 && args[1] == "--agent" {
			targetAgent = contracts.AgentType(args[2])
		} else if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: coconet mcp [--agent codex|claude|cursor]")
			return 2
		}
		if targetAgent != "" && !contracts.ValidAgent(targetAgent) {
			fmt.Fprintln(stderr, "usage: coconet mcp [--agent codex|claude|cursor]")
			return 2
		}
		// Cursor can discover the installed Claude plugin as well. Its native
		// MCP environment identifies the actual continuation host.
		if os.Getenv("CURSOR_AGENT") == "1" {
			targetAgent = contracts.AgentCursor
		}
		if err := serveMCP(ctx, stdin, stdout, targetAgent); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "Unknown command %q. Run `coconet --help` to see available commands.\n", args[0])
		return 2
	}
}

func shouldAutoSyncAgentIntegrations(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "init", "connect":
		return true
	default:
		return false
	}
}
