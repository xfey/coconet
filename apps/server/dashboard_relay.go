package main

import (
	"encoding/json"
	"github.com/xfey/coconet/packages/contentstore"
	"net/http"

	"github.com/xfey/coconet/packages/contracts"
)

// The hosted page receives a client-generated cache. This handler neither
// invokes the semantic server nor opens any Session or processing archive.
func (h *serverHandler) servePublishedGraph(w http.ResponseWriter, r *http.Request, requestID string, actor contracts.Account, project string) {
	var legacyRevision int64
	authors := map[string]string{}
	err := h.authority.store.view(func(state databaseState) error {
		if _, _, err := requireReadableMember(&state, actor.AccountID, project); err != nil {
			return err
		}
		legacyRevision = dashboardRevision(state, project)
		for _, member := range state.Memberships.By("project_id", project) {
			if member.ProjectID != project {
				continue
			}
			if account, ok := state.Accounts.Lookup(member.Account.AccountID); ok {
				authors[account.AccountID] = account.DisplayName
			}
		}
		return nil
	})
	if err != nil {
		h.writeError(w, requestID, err)
		return
	}
	storage, err := h.authority.store.dashboardRelayStore()
	if err != nil {
		h.writeError(w, requestID, unavailable("relay_storage_unavailable", "published graph storage is unavailable"))
		return
	}
	head, err := storage.CheckpointHead(project)
	if err != nil {
		h.writeError(w, requestID, unavailable("relay_storage_unavailable", "published graph metadata is unavailable"))
		return
	}
	etag := publishedGraphETag(legacyRevision, head.Head, head.Generation, authors)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	page := contracts.RelayCheckpointPage{SchemaVersion: 1}
	if storage != nil {
		page, err = storage.Checkpoint(project)
	}
	if err != nil {
		h.writeError(w, requestID, unavailable("relay_storage_unavailable", "published graph could not be read"))
		return
	}
	w.Header().Set("ETag", publishedGraphETag(legacyRevision, page.Head, page.Generation, authors))
	h.writeJSON(w, 200, map[string]any{"schema_version": 1, "relay": page, "event_revision": legacyRevision + page.Generation, "authors": authors})
}

func publishedGraphETag(revision, head, generation int64, authors map[string]string) string {
	data, _ := json.Marshal([]any{revision, head, generation, authors})
	return `"` + contentstore.ObjectDigest(data) + `"`
}
