package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/inference"
	"github.com/xfey/coconet/packages/localdag"
)

// This explicit local entry is also the offline debugging/replay interface.
// It does not select the Hosted deployment or mutate the user's Agent history.
func runLocalDAG(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: coconet local-dag <serve|status|stop|observe|capture|process|sync|state|pull|logs|proposals|transfers|tasks|retry|inbox|retry-inbox|replay> --root DIR [--project ID] [options]")
		return 2
	}
	flags := flag.NewFlagSet("local-dag", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "private local DAG store directory")
	project := flags.String("project", "", "project identity")
	actor := flags.String("actor", "", "origin device identity")
	input := flags.String("input", "", "fixed capture JSON file (read-only)")
	provider := flags.String("provider", "", "codex, claude or cursor")
	command := flags.String("command", "", "local provider executable")
	profile := flags.String("profile", "", "dedicated inference configuration root")
	model := flags.String("model", "", "explicit inference model")
	budget := flags.Int("max-calls", 0, "maximum model attempts (process: this command; serve: rolling 24 hours across restarts)")
	timeout := flags.Duration("timeout", 120*time.Second, "hard deadline per provider call")
	taskID := flags.String("task", "", "exact paused task or inbox event identity")
	nodeID := flags.String("node", "", "work node selected from the accepted project graph")
	native := flags.Bool("native", false, "also materialize the original artifact for explicit native continuation")
	relayConfig := flags.String("relay-config", "", "private Relay configuration (defaults to ROOT/relay.json)")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || !filepath.IsAbs(*root) || *project == "" && args[0] != "serve" && args[0] != "status" && args[0] != "stop" && args[0] != "inbox" && args[0] != "retry-inbox" {
		return 2
	}
	if args[0] != "serve" {
		options := localDAGClientOptions{Project: *project, Input: *input, Task: *taskID, Node: *nodeID, Native: *native}
		if handled, code := localDAGClient(ctx, *root, args[0], options, stdout, stderr); handled {
			return code
		}
	}
	if args[0] == "status" || args[0] == "stop" {
		if err := json.NewEncoder(stdout).Encode(map[string]string{"status": "stopped"}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	store, err := graphstore.Open(*root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer store.Close()
	engine := &localdag.Engine{Store: store, Actor: *actor, Provider: *provider, Model: *model}
	if args[0] == "capture" || args[0] == "observe" || args[0] == "process" || args[0] == "serve" || args[0] == "sync" || args[0] == "retry" || args[0] == "pull" {
		if err := configureLocalDAGRelay(*root, *relayConfig, *project, engine); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	var result any
	switch args[0] {
	case "index", "index-rebuild":
		if args[0] == "index-rebuild" {
			err = store.ResetSearchIndex(ctx, *project)
		}
		if err == nil {
			result, err = store.IndexProject(ctx, *project, 64)
		}
	case "pull":
		if *nodeID == "" {
			fmt.Fprintln(stderr, "pull requires --node selected from the project graph")
			return 2
		}
		result, err = engine.ReadNode(ctx, *project, *nodeID, *native)
	case "observe":
		var data []byte
		data, err = readLocalPrivateFile(*input, 64<<10, false)
		if err != nil {
			break
		}
		var request localDAGObservationRequest
		if err = decodeStrict(data, &request); err != nil {
			break
		}
		if request.ProjectID != *project {
			err = fmt.Errorf("observation project differs from --project")
			break
		}
		result, err = observeLocalDAG(ctx, *root, engine, request)
	case "capture":
		if engine.Actor == "" || *input == "" {
			fmt.Fprintln(stderr, "capture requires --actor and --input")
			return 2
		}
		file, openErr := os.Open(*input)
		if openErr != nil {
			err = openErr
			break
		}
		defer file.Close()
		data, readErr := io.ReadAll(io.LimitReader(file, 64<<20+1))
		if readErr != nil {
			err = readErr
			break
		}
		if len(data) > 64<<20 {
			err = fmt.Errorf("capture file exceeds size limit")
			break
		}
		var capture localdag.Capture
		if err = decodeStrict(data, &capture); err != nil {
			break
		}
		if capture.ProjectID != *project {
			err = fmt.Errorf("capture project differs from --project")
			break
		}
		result, err = captureLocalDAG(ctx, *root, engine, capture)
	case "process", "serve":
		if engine.Actor == "" || *budget < 1 || *budget > 100 || *provider == "" || *command == "" || !filepath.IsAbs(*profile) || *model == "" {
			fmt.Fprintln(stderr, "process requires --actor, --provider, --command, --profile, --model and --max-calls (1–100)")
			return 2
		}
		engine.Runner = inference.Local{Config: inference.Config{Provider: *provider, Command: *command, Model: *model, ConfigRoot: *profile, WorkRoot: filepath.Join(*root, "inference-work"), Timeout: *timeout}}
		if args[0] == "serve" {
			err = serveLocalDAG(ctx, *root, engine, *budget, stdout)
			break
		}
		// Historical import is owned by the managed worker. An explicitly
		// configured offline model must not bypass its pinned provider/budget.
		plans, readErr := readHistoryPlans(*root)
		if readErr != nil {
			err = readErr
			break
		}
		for _, plan := range plans {
			if plan.ProjectID == *project && plan.Status != "preview" && plan.Status != "complete" {
				err = fmt.Errorf("resume this history plan through the managed Runtime before offline processing")
				break
			}
		}
		if err != nil {
			break
		}
		if err = engine.Recover(ctx, *project); err == nil {
			err = engine.Drain(ctx, *project, *budget)
		}
		result, _ = store.Tasks(*project)
	case "state":
		result, err = store.State(*project)
	case "logs":
		result, err = store.Runs(*project)
	case "tasks":
		result, err = store.Tasks(*project)
	case "inbox":
		result, err = localDAGInbox(*root)
	case "retry-inbox":
		err = retryLocalDAGInboxProject(*root, *taskID, *project)
		if err == nil {
			result = map[string]bool{"queued": true}
		}
	case "proposals":
		result, err = store.Proposals(*project)
	case "transfers":
		result, err = store.Transfers(*project, 100)
	case "sync":
		if err = syncLocalDAG(ctx, engine, *project); err == nil {
			err = engine.Flush(ctx, *project)
		}
		result, _ = store.State(*project)
	case "replay":
		result, err = store.Replay(*project)
	case "retry":
		if *taskID == "" {
			fmt.Fprintln(stderr, "retry requires --task with an exact paused task identity")
			return 2
		}
		result, err = engine.Retry(*project, *taskID)
	default:
		fmt.Fprintln(stderr, "unknown local-dag command")
		return 2
	}
	if result != nil {
		if encodeErr := json.NewEncoder(stdout).Encode(result); encodeErr != nil {
			fmt.Fprintln(stderr, encodeErr)
			return 1
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
