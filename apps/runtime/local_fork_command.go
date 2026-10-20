package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/workgraph"
)

func runNativeForkCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("fork", flag.ContinueOnError)
	flags.SetOutput(stderr)
	agent := flags.String("agent", "", "target Agent: codex or claude; cross-Agent Fork imports main dialogue")
	node := flags.String("node", "", "selected DAG node")
	session := flags.String("session", "", "selected local Session identity")
	version := flags.Int("version", 0, "selected fixed version")
	request := flags.String("request-id", "", "stable request ID; retry with all original arguments")
	asJSON := flags.Bool("json", false, "write structured native Fork result")
	if flags.Parse(args) != nil || flags.NArg() != 0 || !contracts.ValidAgent(contracts.AgentType(*agent)) || *session == "" || *version < 1 {
		fmt.Fprintln(stderr, "usage: coconet fork --agent codex|claude|cursor [--node ID] --session ID --version N [--request-id ID] [--json]")
		return 2
	}
	project, managed, err := resolveLocalMCPProject()
	if err != nil || !managed {
		fmt.Fprintln(stderr, "Connect this folder to a local DAG project before creating a native Fork.")
		return 1
	}
	cwd, err := os.Getwd()
	if err == nil {
		cwd, err = canonicalPath(cwd)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	profile, err := canonicalPath(libraryConfigRoot(contracts.AgentType(*agent)))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *request == "" {
		*request = newRequestID()
	}
	fmt.Fprintf(stderr, "Native Fork request: %s. If interrupted, retry with this ID and the same Agent, node, source and workspace.\n", *request)
	operation := &localNativeForkRequest{localForkSelection: localForkSelection{RequestID: *request, NodeID: *node, Source: workgraph.Source{SessionID: *session, Version: *version}}, ProjectID: project.project, Agent: contracts.AgentType(*agent), CWD: cwd, ConfigRoot: profile}
	ctx, cancel := context.WithTimeout(ctx, localNativeForkTimeout)
	defer cancel()
	value, err := project.call(ctx, "fork", localDAGClientOptions{Fork: operation})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *asJSON {
		if _, err := fmt.Fprintln(stdout, string(value)); err != nil {
			return 1
		}
		return 0
	}
	var result localNativeForkResult
	if err := json.Unmarshal(value, &result); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	writeCLISuccess(stdout, "Created a separate native Session")
	if result.TransferFormat != "" {
		fmt.Fprintf(stdout, "Imported %s dialogue into %s. Tools, reasoning and media were not converted.\n", result.SourceAgent, result.Agent)
	}
	fmt.Fprintln(stdout, "Ready to continue locally. Project synchronization runs in the background.")
	fmt.Fprintln(stdout, result.Resume.Display)
	return 0
}
