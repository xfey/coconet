package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"time"
)

// Notifications invalidate a committed snapshot. A bounded mailbox coalesces
// bursts; the durable revision repairs disconnects and process restarts.
type dashboardEventHub struct {
	mu          sync.Mutex
	subscribers map[chan struct{}]string
}

func newDashboardEventHub() *dashboardEventHub {
	return &dashboardEventHub{subscribers: make(map[chan struct{}]string)}
}

func (hub *dashboardEventHub) subscribe(project string) (<-chan struct{}, func(), bool) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.subscribers) >= 4096 {
		return nil, func() {}, false
	}
	channel := make(chan struct{}, 1)
	hub.subscribers[channel] = project
	return channel, func() { hub.mu.Lock(); delete(hub.subscribers, channel); hub.mu.Unlock() }, true
}

func (hub *dashboardEventHub) publish(projects map[string]bool, access bool) {
	if hub == nil {
		return
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	for channel, project := range hub.subscribers {
		if !access && !projects[project] {
			continue
		}
		select {
		case channel <- struct{}{}:
		default:
		}
	}
}

func dashboardRevision(state databaseState, project string) int64 {
	return max(1, state.DashboardRevisions.Get(project))
}

func markDashboardChanged(state *databaseState, project string) {
	if state.activityMigration || project == "" {
		return
	}
	if state.pendingDashboardChanges == nil {
		state.pendingDashboardChanges = make(map[string]bool)
	}
	state.pendingDashboardChanges[project] = true
}

func setWorkNode(state *databaseState, node workNodeRecord) {
	if old, exists := state.WorkNodes.Lookup(node.WorkNodeID); exists && reflect.DeepEqual(old, node) {
		return
	}
	state.WorkNodes.Set(node.WorkNodeID, node)
	markDashboardChanged(state, node.ProjectID)
}

func finalizeDashboardChanges(state *databaseState) {
	for project := range state.pendingDashboardChanges {
		state.DashboardRevisions.Set(project, dashboardRevision(*state, project)+1)
	}
}

func (a *authority) dashboardEventState(token, project string) (int64, time.Duration, error) {
	var revision int64
	var remaining time.Duration
	err := a.store.view(func(state databaseState) error {
		session, exists := state.DashboardSessions.Lookup(hashToken(token))
		expiry, err := time.Parse(time.RFC3339Nano, session.ExpiresAt)
		remaining = expiry.Sub(a.dashboardTime(state))
		if !exists || err != nil || remaining <= 0 {
			return dashboardAuthError()
		}
		if exists := state.Accounts.Has(session.AccountID); !exists {
			return dashboardAuthError()
		}
		if _, _, err := requireReadableMember(&state, session.AccountID, project); err != nil {
			return err
		}
		revision = dashboardRevision(state, project)
		return nil
	})
	if err == nil {
		storage, openErr := a.store.dashboardRelayStore()
		if openErr != nil {
			return 0, remaining, openErr
		}
		if storage == nil {
			return revision, remaining, nil
		}
		generation, readErr := storage.Generation(project)
		if readErr != nil {
			return 0, remaining, readErr
		}
		revision += generation
	}
	return revision, remaining, err
}

func (h *serverHandler) serveDashboardEvents(w http.ResponseWriter, r *http.Request, requestID, project, token string) {
	updates, unsubscribe, ok := h.authority.store.events.subscribe(project)
	if !ok {
		h.writeError(w, requestID, unavailable("event_connections_exhausted", "too many dashboard connections"))
		return
	}
	defer unsubscribe()
	// Subscribe before reading: a commit racing this read stays in the mailbox.
	revision, remaining, err := h.authority.dashboardEventState(token, project)
	if err != nil {
		h.writeError(w, requestID, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	write := func(event string, revision int64) error {
		_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
		data, _ := json.Marshal(map[string]any{"revision": revision})
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return err
		}
		return controller.Flush()
	}
	defer controller.SetWriteDeadline(time.Time{})
	if err := write("graph", revision); err != nil {
		return
	}
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	// The authority may use the database clock (or an injected test clock).
	// Translate its remaining TTL to a duration instead of mixing clock origins.
	expires := time.NewTimer(remaining)
	defer expires.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-expires.C:
			_ = write("access_revoked", 0)
			return
		case <-heartbeat.C:
			if err := write("heartbeat", revision); err != nil {
				return
			}
		case <-updates:
			latest, _, err := h.authority.dashboardEventState(token, project)
			if err != nil {
				_ = write("access_revoked", 0)
				return
			}
			if latest != revision {
				if err := write("graph", latest); err != nil {
					return
				}
				revision = latest
			}
		}
	}
}
