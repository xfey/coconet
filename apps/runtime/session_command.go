package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/workgraph"
)

func runSessionCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runSessionCommandWithAdapters(ctx, args, stdout, stderr, nil)
}

func runSessionCommandWithAdapters(ctx context.Context, args []string, stdout, stderr io.Writer, configured []adapters.Adapter) int {
	ctx, cancel := context.WithTimeout(ctx, mcpHandoffTimeout)
	defer cancel()
	if len(args) == 0 || (args[0] != "read" && args[0] != "pull" && args[0] != "resume") {
		fmt.Fprintln(stderr, "usage: coconet session <read|pull|resume> --project ID [--node ID] --session ID --version N [--agent codex|claude|cursor]")
		return 2
	}
	flags := flag.NewFlagSet("session", flag.ContinueOnError)
	flags.SetOutput(stderr)
	project := flags.String("project", "", "expected Project")
	session := flags.String("session", "", "fixed Session")
	version := flags.Int("version", 0, "fixed Version")
	agent := flags.String("agent", "", "native Agent type for resume")
	node := flags.String("node", "", "selected Work Node")
	raw := flags.Bool("include-artifact", false, "also download native artifact")
	requestID := flags.String("request-id", "", "stable native continuation request; repeat with the original source")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || *project == "" || *session == "" || *version < 1 || (*agent != "" && args[0] != "resume") || (args[0] == "resume" && !contracts.ValidAgent(contracts.AgentType(*agent))) {
		fmt.Fprintln(stderr, "Specify --project, --session and a positive --version. For resume, also specify --agent codex or claude.")
		return 2
	}
	if local, managed, err := resolveLocalMCPProject(); managed {
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if local.project != *project {
			fmt.Fprintln(stderr, "This folder is connected to a different project.")
			return 1
		}
		if args[0] == "resume" {
			forkArgs := []string{"--agent", *agent, "--node", *node, "--session", *session, "--version", fmt.Sprint(*version)}
			if *requestID != "" {
				forkArgs = append(forkArgs, "--request-id", *requestID)
			}
			return runNativeForkCommand(ctx, forkArgs, stdout, stderr)
		}
		value, err := local.call(ctx, "pull", localDAGClientOptions{Node: *node, Source: &workgraph.Source{SessionID: *session, Version: *version}, Native: *raw})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if args[0] == "pull" {
			if _, err := fmt.Fprintln(stdout, string(value)); err != nil {
				return 1
			}
			return 0
		}
		var read localdag.NodeRead
		if err := json.Unmarshal(value, &read); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		file, err := os.Open(read.CompactPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		defer file.Close()
		if _, err := io.Copy(stdout, file); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	fmt.Fprintln(stderr, "Run coconet connect in this folder first.")
	return 1
}
