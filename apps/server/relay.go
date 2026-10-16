package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/relay"
	"github.com/xfey/coconet/packages/storageguard"
)

func (s *store) relayStore() (*relay.Store, error) {
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	if s.relay != nil {
		return s.relay, nil
	}
	path := ""
	if s.db != nil {
		path = s.db.Path()
	}
	root, err := relayRootForDatabase(path)
	if err != nil {
		return nil, err
	}
	s.relay, err = openRelayStorage(root, s.capacity)
	if err == nil {
		ctx, cancel := context.WithCancel(context.Background())
		s.relayCancel = cancel
		s.relayDone = make(chan struct{})
		storage := s.relay
		go func() {
			defer close(s.relayDone)
			storage.RunMaintenance(ctx, func(err error) {
				if err != nil && ctx.Err() == nil {
					log.Printf("Relay temporary storage maintenance failed: %v", err)
				}
			})
		}()
	}
	return s.relay, err
}

func relayRootForDatabase(database string) (string, error) {
	root := os.Getenv("COCONET_RELAY_HOME")
	if root == "" && database != "" {
		root = database + ".relay"
	}
	if !filepath.IsAbs(root) {
		return "", errors.New("Relay requires an absolute COCONET_RELAY_HOME with the PostgreSQL authority backend")
	}
	return filepath.Clean(root), nil
}

func (s *store) dashboardRelayStore() (*relay.Store, error) { return s.relayStore() }

func (h *serverHandler) handleRelay(w http.ResponseWriter, r *http.Request, requestID string, actor contracts.Account, project string, parts []string) {
	err := h.authority.store.view(func(state databaseState) error {
		if r.Method == http.MethodGet || len(parts) > 0 && parts[0] == "leases" || len(parts) == 2 && parts[0] == "repairs" && parts[1] == "request" || len(parts) == 2 && parts[0] == "transfers" && parts[1] == "download" {
			_, _, err := requireReadableMember(&state, actor.AccountID, project)
			return err
		}
		_, _, err := requireActiveMember(&state, actor.AccountID, project)
		return err
	})
	if err != nil {
		h.writeError(w, requestID, err)
		return
	}
	storage, err := h.authority.store.relayStore()
	if err != nil {
		h.writeError(w, requestID, err)
		return
	}
	writeFailure := func(err error) {
		switch {
		case errors.Is(err, storageguard.ErrCapacity), errors.Is(err, storageguard.ErrSpace):
			h.writeError(w, requestID, err)
		case errors.Is(err, contracts.ErrRelayHistoryCompacted):
			h.writeError(w, requestID, &authorityError{HTTPStatus: http.StatusGone, Status: contracts.StatusUnavailable, Code: "relay_history_compacted", Message: "older history has been replaced by a checkpoint; update the Coconet client to recover and continue syncing"})
		case errors.Is(err, contracts.ErrRelayReceiptMissing):
			h.writeError(w, requestID, notFound("relay_receipt_missing", "commit was not accepted"))
		case errors.Is(err, contracts.ErrProcessingBusy):
			h.writeError(w, requestID, conflict("processing_busy", "another attempt is processing this conversation"))
		case errors.Is(err, contracts.ErrProcessingStale):
			h.writeError(w, requestID, conflict("processing_stale", "task or execution claim expired; synchronize before retrying"))
		case errors.Is(err, contracts.ErrProcessingYielded):
			h.writeError(w, requestID, conflict("processing_yielded", "this device yielded to another device; explicit retry is also available"))
		case errors.Is(err, contracts.ErrProcessingPaused):
			h.writeError(w, requestID, conflict("processing_paused", "shared retry attempts exhausted; explicit retry required"))
		case errors.Is(err, relay.ErrConflict):
			h.writeError(w, requestID, conflict("relay_revision_conflict", "project changed; synchronize before retrying"))
		case errors.Is(err, relay.ErrObject):
			h.writeError(w, requestID, notFound("relay_object_missing", "required project content has not been uploaded"))
		case errors.Is(err, relay.ErrBusy):
			h.writeError(w, requestID, conflict("relay_content_busy", "content is protected by an active reader or upload"))
		case errors.Is(err, relay.ErrGrace):
			h.writeError(w, requestID, conflict("relay_collection_grace", "content protection period has not elapsed"))
		case errors.Is(err, relay.ErrUploading):
			h.writeError(w, requestID, conflict("relay_upload_in_progress", "this object upload is already in progress"))
		case errors.Is(err, relay.ErrInvalid), errors.Is(err, contentstore.ErrInput):
			h.writeError(w, requestID, invalid("relay_request_rejected", err.Error()))
		case errors.Is(err, relay.ErrQuota):
			h.writeError(w, requestID, &authorityError{HTTPStatus: http.StatusInsufficientStorage, Status: contracts.StatusUnavailable, Code: "relay_storage_quota", Message: "project storage quota exceeded"})
		case errors.Is(err, relay.ErrLogQuota):
			h.writeError(w, requestID, &authorityError{HTTPStatus: http.StatusInsufficientStorage, Status: contracts.StatusUnavailable, Code: "relay_log_quota", Message: "project commit log quota exceeded; contact the deployment administrator before resuming publication"})
		default:
			h.writeError(w, requestID, unavailable("relay_storage_unavailable", "relay storage could not complete the request"))
		}
	}
	if len(parts) == 2 && parts[0] == "receipts" && r.Method == http.MethodGet {
		result, err := storage.Receipt(project, parts[1])
		if err != nil {
			writeFailure(err)
		} else {
			h.writeJSON(w, 200, result)
		}
		return
	}
	if h.serveRelayProcessing(w, r, requestID, actor, project, parts, storage, writeFailure) {
		return
	}
	if h.serveRelayRepair(w, r, requestID, actor, project, parts, storage, writeFailure) {
		return
	}
	if h.serveRelayTransfer(w, r, requestID, actor, project, parts, storage, writeFailure) {
		return
	}
	if len(parts) == 1 && parts[0] == "objects" && r.Method == http.MethodGet {
		limit := 512
		if value := r.URL.Query().Get("limit"); value != "" {
			limit, err = strconv.Atoi(value)
			if err != nil {
				writeFailure(fmt.Errorf("%w: invalid catalog page limit", relay.ErrInvalid))
				return
			}
		}
		page, err := storage.ObjectCatalog(project, r.URL.Query().Get("after"), limit)
		if err != nil {
			writeFailure(err)
			return
		}
		h.writeJSON(w, http.StatusOK, page)
		return
	}
	if len(parts) > 0 && (parts[0] == "leases" || parts[0] == "collections") {
		switch {
		case len(parts) == 1 && parts[0] == "leases" && r.Method == http.MethodPost:
			var input contracts.RelayReadLeaseRequest
			if err := decodeRelayRetention(w, r, &input); err != nil {
				writeFailure(err)
				return
			}
			result, err := storage.ReadLease(project, actor.AccountID, input)
			if err != nil {
				writeFailure(err)
				return
			}
			h.writeJSON(w, 200, result)
		case len(parts) == 2 && parts[0] == "leases" && r.Method == http.MethodDelete:
			if err := storage.ReleaseReadLease(project, actor.AccountID, r.URL.Query().Get("device"), parts[1]); err != nil {
				writeFailure(err)
				return
			}
			h.writeJSON(w, 200, map[string]bool{"released": true})
		case len(parts) == 1 && parts[0] == "collections" && r.Method == http.MethodPost:
			var input contracts.RelayCollectionPlan
			if err := decodeRelayRetention(w, r, &input); err != nil {
				writeFailure(err)
				return
			}
			result, err := storage.MarkCollection(project, actor.AccountID, input)
			if err != nil {
				writeFailure(err)
				return
			}
			h.writeJSON(w, 200, result)
		case len(parts) == 2 && parts[0] == "collections" && r.Method == http.MethodPost:
			result, err := storage.Collect(project, actor.AccountID, r.URL.Query().Get("device"), parts[1])
			if err != nil {
				writeFailure(err)
				return
			}
			h.writeJSON(w, 200, result)
		default:
			h.writeError(w, requestID, notFound("relay_route_not_found", "unknown retention operation"))
		}
		return
	}
	if len(parts) == 1 && parts[0] == "checkpoint-head" && r.Method == http.MethodGet {
		head, err := storage.CheckpointHead(project)
		if err != nil {
			writeFailure(err)
		} else {
			h.writeJSON(w, 200, head)
		}
		return
	}
	if len(parts) == 1 && parts[0] == "checkpoint" {
		switch r.Method {
		case http.MethodGet:
			page, err := storage.Checkpoint(project)
			if err != nil {
				writeFailure(err)
				return
			}
			h.writeJSON(w, 200, page)
		case http.MethodPost:
			var input contracts.RelayCheckpoint
			if err := decodeJSONLimit(r, &input, contracts.MaxRelayCheckpointEnvelopeBytes); err != nil {
				writeFailure(fmt.Errorf("%w: invalid checkpoint JSON", relay.ErrInvalid))
				return
			}
			receipt, err := storage.PutCheckpoint(project, actor.AccountID, input)
			if err != nil {
				writeFailure(err)
				return
			}
			h.authority.store.events.publish(map[string]bool{project: true}, false)
			h.writeJSON(w, 200, receipt.Acknowledgement())
		default:
			h.methodNotAllowed(w, requestID, "GET, POST")
		}
		return
	}
	if len(parts) == 2 && parts[0] == "objects" && parts[1] == "check" && r.Method == http.MethodPost {
		var input contracts.RelayObjectCheck
		if err := decodeJSONLimit(r, &input, 1<<20); err != nil {
			writeFailure(fmt.Errorf("%w: invalid object check JSON", relay.ErrInvalid))
			return
		}
		result, err := storage.Missing(project, input)
		if err != nil {
			writeFailure(err)
			return
		}
		h.writeJSON(w, 200, result)
		return
	}
	if len(parts) == 1 && parts[0] == "commits" {
		switch r.Method {
		case http.MethodGet:
			after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
			if err != nil {
				writeFailure(fmt.Errorf("%w: after must be a nonnegative revision", relay.ErrInvalid))
				return
			}
			limit := 100
			if value := r.URL.Query().Get("limit"); value != "" {
				limit, err = strconv.Atoi(value)
				if err != nil {
					writeFailure(fmt.Errorf("%w: invalid page limit", relay.ErrInvalid))
					return
				}
			}
			page, err := storage.Since(project, after, limit)
			if err != nil {
				writeFailure(err)
				return
			}
			h.writeJSON(w, 200, page)
		case http.MethodPost:
			var input contracts.RelayCommit
			if err := decodeJSONLimit(r, &input, 2<<20); err != nil {
				writeFailure(fmt.Errorf("%w: invalid commit JSON", relay.ErrInvalid))
				return
			}
			receipt, err := storage.Append(project, actor.AccountID, input)
			if err != nil {
				writeFailure(err)
				return
			}
			h.writeJSON(w, 200, receipt)
		default:
			h.methodNotAllowed(w, requestID, "GET, POST")
		}
		return
	}
	if len(parts) == 2 && parts[0] == "objects" {
		digest := parts[1]
		switch r.Method {
		case http.MethodPut:
			body := http.MaxBytesReader(w, r.Body, contentstore.MaxObjectBytes)
			if err := storage.PutObject(project, digest, r.ContentLength, body); err != nil {
				writeFailure(err)
				return
			}
			h.writeJSON(w, 200, map[string]any{"schema_version": 1, "sha256": digest})
		case http.MethodGet:
			file, err := storage.Object(project, digest)
			if err != nil {
				writeFailure(err)
				return
			}
			defer file.Close()
			info, err := file.Stat()
			if err != nil {
				writeFailure(err)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
			w.Header().Set("X-Content-SHA256", digest)
			_, _ = io.Copy(w, file)
		default:
			h.methodNotAllowed(w, requestID, "GET, PUT")
		}
		return
	}
	h.writeError(w, requestID, notFound("relay_route_not_found", "relay route was not found"))
}

func decodeRelayRetention(w http.ResponseWriter, r *http.Request, output any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(output) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("%w: invalid retention request", relay.ErrInvalid)
	}
	return nil
}
