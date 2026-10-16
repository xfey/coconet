package main

import (
	"context"
	"errors"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
)

type previewAdapter struct {
	agent    contracts.AgentType
	sessions []contracts.DiscoveredSession
	err      error
}

func (adapter previewAdapter) Type() contracts.AgentType { return adapter.agent }
func (adapter previewAdapter) Health(context.Context) contracts.ComponentHealth {
	return contracts.ComponentHealth{Name: "preview.fake", Status: contracts.StatusOK}
}
func (adapter previewAdapter) Discover(context.Context, string) ([]contracts.DiscoveredSession, error) {
	return adapter.sessions, adapter.err
}
func (adapter previewAdapter) Observe(context.Context, string, string, adapters.ObservationCheckpoint) (contracts.SessionObservation, adapters.ObservationCheckpoint, error) {
	return contracts.SessionObservation{}, adapters.ObservationCheckpoint{}, errors.New("not used")
}
