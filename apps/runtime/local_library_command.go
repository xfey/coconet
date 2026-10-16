package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/workgraph"
)

func runLocalLibraryCommand(ctx context.Context, project localMCPProject, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "list" && args[0] != "add" && args[0] != "remove" {
		fmt.Fprintln(stderr, "usage: coconet library list [--limit 50 --cursor CURSOR] | add|remove --node ID --session ID --version N --request-id ID [--note TEXT (add only)]")
		return 2
	}
	flags := flag.NewFlagSet("library "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	node := flags.String("node", "", "selected Work Node")
	session := flags.String("session", "", "selected local Session identity")
	version := flags.Int("version", 0, "selected fixed version")
	request := flags.String("request-id", "", "stable request ID; repeat with all original arguments")
	note := flags.String("note", "", "optional collection purpose")
	limit := flags.Int("limit", 50, "maximum nodes per page, 1–100")
	cursor := flags.String("cursor", "", "next_cursor from the same selection")
	asJSON := flags.Bool("json", false, "write structured result")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || *limit < 1 || *limit > 100 {
		return 2
	}
	options := localDAGClientOptions{Limit: *limit, Cursor: *cursor}
	action := "library"
	if args[0] == "add" || args[0] == "remove" {
		if *node == "" || *session == "" || *version < 1 {
			fmt.Fprintln(stderr, "Select --node, --session and --version from the local project graph.")
			return 2
		}
		if *request == "" {
			*request = newRequestID()
		}
		options.Library = &localdag.LibraryRequest{RequestID: *request, NodeID: *node, Source: workgraph.Source{SessionID: *session, Version: *version}, Note: *note}
		fmt.Fprintf(stderr, "Collection request: %s. If interrupted, retry with this ID and the same node, source and note.\n", *request)
		action = "collect"
		if args[0] == "remove" {
			action = "uncollect"
		}
	}
	value, err := project.call(ctx, action, options)
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
	if action == "collect" {
		writeCLISuccess(stdout, "Saved the selected fixed source to Session Library")
		return 0
	}
	if action == "uncollect" {
		writeCLISuccess(stdout, "Removed the Library entry; the DAG node and fixed source are preserved")
		return 0
	}
	var graph localdag.GraphView
	if err := json.Unmarshal(value, &graph); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(graph.Nodes) == 0 {
		fmt.Fprintln(stdout, "No collected nodes on this page.")
	}
	for _, node := range graph.Nodes {
		fmt.Fprintf(stdout, "%s\n  %s · %s · version %d\n", node.Objective, node.ID, node.Source.SessionID, node.Source.Version)
	}
	if graph.NextCursor != "" {
		fmt.Fprintf(stdout, "Next page:\n  %s\n", shellCommand("coconet", []string{"library", "list", "--cursor", graph.NextCursor, "--limit", fmt.Sprint(*limit)}))
	}
	return 0
}
