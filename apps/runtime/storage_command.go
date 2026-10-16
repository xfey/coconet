package main

import (
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
	"github.com/xfey/coconet/packages/localdag"
)

func runStorage(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: coconet storage <status|configure|prune|reindex|export|restore> [--max-mib 16384] [--exports-mib 1024]")
		return 2
	}
	flags := flag.NewFlagSet("storage", flag.ContinueOnError)
	flags.SetOutput(stderr)
	project := flags.String("project", "", "project for explicit-root storage operations")
	archive := flags.String("file", "", "absolute path to a private replica archive")
	root := flags.String("root", "", "explicit private Runtime store (normally selected from this folder)")
	mib := flags.Int64("max-mib", 16384, "maximum stored session content, shared by this account and deployment")
	exports := flags.Int64("exports-mib", 1024, "maximum derived session files, separate from content blocks")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
		return 2
	}
	if args[0] != "status" && args[0] != "configure" && args[0] != "prune" && args[0] != "reindex" && args[0] != "export" && args[0] != "restore" {
		return 2
	}
	if *root == "" {
		_, binding, paths, err := currentProjectBinding()
		if err != nil || binding == nil || binding.Workflow != localDAGWorkflow {
			fmt.Fprintln(stderr, "Connect this folder to a Relay project before managing local storage.")
			return 1
		}
		*root = localDAGBindingRoot(paths, *binding)
		*project = binding.ProjectID
	}
	if !filepath.IsAbs(*root) || *mib < 16 || *mib > 1048576 || *exports < 16 || *exports > 1048576 {
		return 2
	}
	if (args[0] == "reindex" || args[0] == "export" || args[0] == "restore") && *project == "" {
		fmt.Fprintln(stderr, "This operation requires a connected folder or an explicit --project")
		return 2
	}
	if (args[0] == "export" || args[0] == "restore") && !filepath.IsAbs(*archive) {
		fmt.Fprintln(stderr, "export and restore require an absolute --file path")
		return 2
	}
	options := localDAGClientOptions{Project: *project, Input: *archive}
	// Omitted limits preserve the existing policy, including online updates.
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "max-mib" {
			options.ObjectLimit = *mib << 20
		}
		if f.Name == "exports-mib" {
			options.ExportLimit = *exports << 20
		}
	})
	action := "storage-" + args[0]
	if handled, code := localDAGClient(ctx, *root, action, options, stdout, stderr); handled {
		return code
	}
	if _, err := os.Stat(filepath.Join(*root, "graph.db")); err != nil && args[0] != "restore" {
		fmt.Fprintln(stderr, "No local processing store is available yet.")
		return 1
	}
	store, err := graphstore.Open(*root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer store.Close()
	engine := &localdag.Engine{Store: store}
	if config, err := loadLocalDAGService(*root); err == nil {
		engine.Actor = config.Actor
	}
	value, err := localStorage(ctx, *root, engine, action, options)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(value); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
func localStorage(ctx context.Context, root string, engine *localdag.Engine, action string, options localDAGClientOptions) (any, error) {
	switch action {
	case "storage-export", "storage-restore":
		started := time.Now()
		var result graphstore.ReplicaArchiveResult
		var err error
		if action == "storage-export" {
			result, err = engine.Store.ExportReplica(ctx, options.Project, options.Input)
		} else {
			result, err = engine.Store.RestoreReplica(ctx, options.Project, options.Input)
		}
		status := "completed"
		message := ""
		if err != nil {
			status = "failed"
			message = err.Error()
		}
		logErr := engine.Store.RecordTransfer(graphstore.Transfer{ProjectID: options.Project, Operation: action, Status: status, Revision: result.Revision, Error: message, Bytes: result.Bytes, DurationMS: time.Since(started).Milliseconds()})
		return result, errors.Join(err, logErr)
	case "storage-status":
		return engine.Store.StorageUsage(ctx)
	case "storage-reindex":
		if options.Project == "" {
			return nil, fmt.Errorf("reindex requires a project")
		}
		if err := engine.Store.ResetSearchIndex(ctx, options.Project); err != nil {
			return nil, err
		}
		return engine.Store.IndexProject(ctx, options.Project, 64)
	case "storage-configure":
		if err := engine.Store.SetStorageLimits(options.ObjectLimit, options.ExportLimit); err != nil {
			return nil, err
		}
		return engine.Store.CacheUsage(), nil
	case "storage-prune":
		result, err := maintainLocalDAGCache(ctx, root, engine)
		return map[string]any{"collection": result, "usage": engine.Store.CacheUsage()}, err
	default:
		return nil, fmt.Errorf("unknown local storage action")
	}
}
