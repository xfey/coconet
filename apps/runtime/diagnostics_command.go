package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/xfey/coconet/packages/graphstore"
)

func runDiagnostics(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: coconet diagnostics <status|list|configure|pin|unpin|export|prune> [--run ID] [--output FILE] [--include-content]")
		return 2
	}
	flags := flag.NewFlagSet("diagnostics", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "explicit private Runtime store (normally selected from this folder)")
	project := flags.String("project", "", "project for an explicit store")
	run := flags.String("run", "", "exact processing run")
	output := flags.String("output", "", "new export file; existing files are never overwritten")
	content := flags.Bool("include-content", false, "include private processing input and model output in this explicitly selected export")
	days := flags.Int("days", 7, "maximum age for unpinned full archives, shared by this account and deployment")
	mib := flags.Int64("max-mib", 256, "maximum local processing archive size, including reserved response space")
	before := flags.String("before", "", "previous diagnostic page cursor")
	limit := flags.Int("limit", 50, "metadata records per page (1–100)")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
		return 2
	}
	if *root == "" {
		_, binding, paths, err := currentProjectBinding()
		if err != nil || binding == nil || binding.Workflow != localDAGWorkflow {
			fmt.Fprintln(stderr, "Connect this folder to a Relay project before inspecting local diagnostics.")
			return 1
		}
		*root = localDAGBindingRoot(paths, *binding)
		*project = binding.ProjectID
	}
	if !filepath.IsAbs(*root) {
		return 2
	}
	action := "diagnostics-" + args[0]
	options := localDAGClientOptions{Project: *project, Task: *run, IncludeContent: *content, Limit: *limit, Query: *before}
	switch args[0] {
	case "status", "prune":
	case "list":
		if *project == "" || *limit < 1 || *limit > 100 {
			return 2
		}
	case "configure":
		if *days < 1 || *days > 365 || *mib < 32 || *mib > 16384 {
			return 2
		}
		options.DiagnosticPolicy = &graphstore.DiagnosticPolicy{Days: *days, MaxBytes: *mib << 20}
	case "pin", "unpin", "export":
		if *project == "" || *run == "" {
			fmt.Fprintln(stderr, "Select --run from coconet diagnostics list.")
			return 2
		}
		if args[0] == "export" && *output == "" {
			fmt.Fprintln(stderr, "Export requires a new --output file; add --include-content only when private prompt content is needed.")
			return 2
		}
	default:
		return 2
	}
	var response boundedLocalResponse
	handled, code := localDAGClient(ctx, *root, action, options, &response, stderr)
	if handled && code != 0 {
		return code
	}
	if !handled {
		if _, err := os.Stat(filepath.Join(*root, "graph.db")); err != nil {
			fmt.Fprintln(stderr, "No local processing store is available yet.")
			return 1
		}
		store, err := graphstore.Open(*root)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		value, err := localDiagnostics(store, action, options)
		store.Close()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := json.NewEncoder(&response).Encode(value); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if args[0] != "export" {
		_, err := stdout.Write(response.Bytes())
		if err != nil {
			return 1
		}
		return 0
	}
	path, err := filepath.Abs(*output)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	_, writeErr := io.Copy(file, bytes.NewReader(response.Bytes()))
	err = errors.Join(writeErr, file.Sync(), file.Close())
	if err != nil {
		os.Remove(path)
		fmt.Fprintln(stderr, err)
		return 1
	}
	var exported struct {
		ContentIncluded bool `json:"content_included"`
	}
	_ = json.Unmarshal(response.Bytes(), &exported)
	json.NewEncoder(stdout).Encode(map[string]any{"exported": path, "run": *run, "content_included": exported.ContentIncluded})
	return 0
}
func localDiagnostics(store *graphstore.Store, action string, options localDAGClientOptions) (any, error) {
	switch action {
	case "diagnostics-status":
		return store.DiagnosticUsage()
	case "diagnostics-list":
		return store.RunSummaries(options.Project, options.Query, options.Limit)
	case "diagnostics-configure":
		if options.DiagnosticPolicy == nil {
			return nil, errors.New("diagnostic retention policy is required")
		}
		if err := store.SetDiagnosticPolicy(*options.DiagnosticPolicy); err != nil {
			return nil, err
		}
		return store.DiagnosticUsage()
	case "diagnostics-pin", "diagnostics-unpin":
		if err := store.PinRun(options.Project, options.Task, action == "diagnostics-pin"); err != nil {
			return nil, err
		}
		return map[string]any{"run": options.Task, "pinned": action == "diagnostics-pin"}, nil
	case "diagnostics-prune":
		if err := store.PruneDiagnostics(time.Now()); err != nil {
			return nil, err
		}
		return store.DiagnosticUsage()
	case "diagnostics-export":
		run, err := store.Run(options.Project, options.Task)
		if err != nil {
			return nil, err
		}
		hashes := map[string]string{}
		if run.Archive == nil {
			hashes["input"] = graphstore.ObjectDigest(run.Input)
			hashes["output"] = graphstore.ObjectDigest([]byte(run.Output))
		}
		included := options.IncludeContent && run.Archive == nil
		if !included {
			run.Input = json.RawMessage(`null`)
			run.Output = ""
			run.Usage = nil
		}
		return map[string]any{"schema": 1, "content_included": included, "content_hashes": hashes, "run": run}, nil
	default:
		return nil, errors.New("unknown diagnostic action")
	}
}
