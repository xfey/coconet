package main

import (
	"errors"
	"net/http"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/relay"
)

func (h *serverHandler) serveRelayRepair(w http.ResponseWriter, r *http.Request, requestID string, actor contracts.Account, project string, parts []string, storage *relay.Store, failure func(error)) bool {
	if len(parts) == 0 || parts[0] != "repairs" {
		return false
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		page, err := storage.Repairs(project, r.URL.Query().Get("after"))
		if err != nil {
			failure(err)
		} else {
			h.writeJSON(w, 200, page)
		}
		return true
	}
	if len(parts) != 2 || r.Method != http.MethodPost {
		h.methodNotAllowed(w, requestID, "GET, POST")
		return true
	}
	var input contracts.RelayRepairRequest
	if err := decodeRelayRetention(w, r, &input); err != nil {
		failure(err)
		return true
	}
	if input.SchemaVersion != 1 {
		failure(relay.ErrInvalid)
		return true
	}
	switch parts[1] {
	case "request":
		// A missing GET may race another uploader. Recheck provider availability;
		// clients cannot invalidate a healthy object just by reporting a failure.
		_, err := storage.DownloadTarget(r.Context(), project, input.SHA256)
		if errors.Is(err, relay.ErrObject) {
			err = storage.RequestRepair(project, input.SHA256)
		}
		if err != nil {
			failure(err)
		} else {
			h.writeJSON(w, 200, map[string]bool{"queued": true})
		}
	case "claim":
		result, err := storage.ClaimRepair(project, actor.AccountID, input)
		if err != nil {
			failure(err)
		} else {
			h.writeJSON(w, 200, result)
		}
	default:
		failure(relay.ErrInvalid)
	}
	return true
}
