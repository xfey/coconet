package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
)

func maintainLocalDAG(ctx context.Context, engine *localdag.Engine, project string) error {
	if err := engine.RetireConsumed(ctx, project); err != nil {
		return err
	}
	if collector, ok := engine.Publisher.(interface {
		CollectUnused(context.Context, string) error
	}); ok {
		return collector.CollectUnused(ctx, project)
	}
	return nil
}

func maintainLocalDAGCache(ctx context.Context, root string, engine *localdag.Engine) (graphstore.CacheCollection, error) {
	lockCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	release, err := acquireLocalFileLock(lockCtx, filepath.Join(root, "inbox-processing.lock"))
	if err != nil {
		if lockCtx.Err() != nil {
			return graphstore.CacheCollection{Busy: true}, nil
		}
		return graphstore.CacheCollection{}, err
	}
	defer release()
	events, err := localDAGInbox(root)
	if err != nil {
		return graphstore.CacheCollection{}, err
	}
	roots := graphstore.CacheRoots{}
	for _, event := range events {
		if event.Prepared == "" {
			continue
		}
		data, err := engine.Store.Object(event.Prepared)
		if err != nil {
			return graphstore.CacheCollection{}, err
		}
		var observation struct {
			ArtifactManifest string `json:"artifact_manifest"`
		}
		if err := json.Unmarshal(data, &observation); err != nil {
			return graphstore.CacheCollection{}, err
		}
		roots.Objects = append(roots.Objects, event.Prepared)
		if observation.ArtifactManifest != "" {
			roots.Artifacts = append(roots.Artifacts, observation.ArtifactManifest)
		}
	}
	forkRoots, err := localForkCacheRoots(root, engine.Store)
	if err != nil {
		return graphstore.CacheCollection{}, err
	}
	roots.Objects = append(roots.Objects, forkRoots.Objects...)
	roots.Artifacts = append(roots.Artifacts, forkRoots.Artifacts...)
	historyRoots, err := localHistoryCacheRoots(ctx, root, engine.Store)
	if err != nil {
		return graphstore.CacheCollection{}, err
	}
	roots.Objects = append(roots.Objects, historyRoots.Objects...)
	roots.Artifacts = append(roots.Artifacts, historyRoots.Artifacts...)
	return engine.Store.PruneCache(ctx, time.Now(), engine.Actor, roots)
}
