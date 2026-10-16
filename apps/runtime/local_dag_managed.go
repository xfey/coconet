package main

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/workgraph"
)

const localDAGReloadExit = 75

func serveConnectedLocalDAG(ctx context.Context, root string, config localDAGServiceConfig, stdout, stderr io.Writer) int {
	store, err := graphstore.Open(root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer store.Close()
	engine := &localdag.Engine{Store: store, Actor: config.Actor, Provider: config.Provider, Model: config.Model}
	if _, err := configureConnectedLocalDAG(root, engine); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if config.PauseReason == "" {
		engine.ResolveRunner = automaticInferenceResolver(root)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var reload atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTicker(time.Second)
		defer timer.Stop()
		baseline := workgraph.Digest(config)
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				current, err := loadLocalDAGService(root)
				if err != nil || workgraph.Digest(current) != baseline {
					reload.Store(err == nil)
					cancel()
					return
				}
			}
		}
	}()
	err = serveLocalDAG(ctx, root, engine, config.MaxCalls, stdout)
	cancel()
	<-done
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if reload.Load() {
		return localDAGReloadExit
	}
	return 0
}
