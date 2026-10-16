package main

import (
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xfey/coconet/packages/contracts"
)

type workspaceRecord struct {
	contracts.WorkspaceReport
	AccountID      string `json:"account_id"`
	CredentialHash string `json:"credential_hash"`
	ReportedAt     string `json:"reported_at"`
}

var installationIDPattern = regexp.MustCompile(`^workspace_[a-f0-9]{32}$`)
var workspaceCursorPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func validWorkspaceText(text string, maximum int) bool {
	return strings.TrimSpace(text) != "" && len(text) <= maximum && utf8.ValidString(text) && strings.IndexFunc(text, unicode.IsControl) < 0
}

func workspaceCredentialActive(state *databaseState, hash, accountID string, now time.Time) bool {
	token, ok := state.Tokens.Lookup(hash)
	return ok && token.AccountID == accountID && (token.DeviceID == "" || authTimeActive(token.ExpiresAt, now))
}

func (a *authority) reportWorkspaces(actor contracts.Account, credential string, input contracts.WorkspaceReport) (contracts.WorkspaceReportResponse, error) {
	result := contracts.WorkspaceReportResponse{SchemaVersion: 1, Status: contracts.StatusOK, Revision: input.Revision}
	if input.SchemaVersion != 1 || !installationIDPattern.MatchString(input.InstallationID) || !validWorkspaceText(input.DeviceName, 120) || input.Revision < 1 || input.Revision > 9007199254740991 || input.Folders == nil || len(input.Folders) > 200 {
		return result, invalid("invalid_workspace_report", "invalid directory snapshot")
	}
	paths := make(map[string]bool)
	for _, folder := range input.Folders {
		_, err := time.Parse(time.RFC3339Nano, folder.ConnectedAt)
		if err != nil || !validWorkspaceText(folder.Path, 4096) || !strings.HasPrefix(folder.Path, "/") || !validWorkspaceText(folder.ProjectID, 128) || paths[folder.Path] {
			return result, invalid("invalid_workspace_report", "invalid or duplicate directory")
		}
		paths[folder.Path] = true
	}
	input.Folders = slices.Clone(input.Folders)
	sort.Slice(input.Folders, func(i, j int) bool { return input.Folders[i].Path < input.Folders[j].Path })
	err := a.store.update(func(state *databaseState) error {
		now := a.dashboardTime(*state)
		if !workspaceCredentialActive(state, credential, actor.AccountID, now) {
			return forbidden("workspace_credential_revoked", "sign in again before reporting directories")
		}
		key := hashToken(actor.AccountID + "\x00" + input.InstallationID)
		old, exists := state.Workspaces.Lookup(key)
		if exists && (input.Revision < old.Revision || input.Revision == old.Revision && (input.DeviceName != old.DeviceName || !slices.Equal(input.Folders, old.Folders))) {
			return conflict("workspace_revision_conflict", "a newer directory snapshot is already registered")
		}
		count := 0
		for id, record := range state.Workspaces.All() {
			if id != key && !workspaceCredentialActive(state, record.CredentialHash, record.AccountID, now) {
				state.Workspaces.Delete(id)
				continue
			}
			if record.AccountID == actor.AccountID {
				count++
			}
		}
		if !exists && count >= 30 {
			return conflict("workspace_limit_reached", "remove an unused device before registering another installation")
		}
		// Keep empty snapshots as revision tombstones. A delayed older report
		// must not resurrect a folder after an offline disconnect.
		state.Workspaces.Set(key, workspaceRecord{WorkspaceReport: input, AccountID: actor.AccountID, CredentialHash: credential, ReportedAt: now.UTC().Format(time.RFC3339Nano)})
		return nil
	})
	return result, err
}

func (a *authority) projectFolders(actor contracts.Account, projectID, cursor string, limit int) (contracts.ProjectFoldersResponse, error) {
	result := contracts.ProjectFoldersResponse{SchemaVersion: 1, Status: contracts.StatusOK, Folders: []contracts.ProjectFolder{}}
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 200 || cursor != "" && !workspaceCursorPattern.MatchString(cursor) {
		return result, invalid("invalid_workspace_cursor", "invalid directory page")
	}
	err := a.store.view(func(state databaseState) error {
		if _, _, err := requireReadableMember(&state, actor.AccountID, projectID); err != nil {
			return err
		}
		for key, record := range state.Workspaces.All() {
			if !workspaceCredentialActive(&state, record.CredentialHash, record.AccountID, a.dashboardTime(state)) {
				continue
			}
			if _, _, err := requireReadableMember(&state, record.AccountID, projectID); err != nil {
				continue
			}
			for _, folder := range record.Folders {
				if folder.ProjectID != projectID {
					continue
				}
				id := hashToken(key + "\x00" + folder.Path)
				if id <= cursor {
					continue
				}
				result.Folders = append(result.Folders, contracts.ProjectFolder{ID: id, AccountID: record.AccountID, DisplayName: state.Accounts.Get(record.AccountID).DisplayName, DeviceName: record.DeviceName, Path: folder.Path, ConnectedAt: folder.ConnectedAt, ReportedAt: record.ReportedAt})
			}
		}
		sort.Slice(result.Folders, func(i, j int) bool { return result.Folders[i].ID < result.Folders[j].ID })
		if len(result.Folders) > limit {
			result.Folders = result.Folders[:limit]
			result.NextCursor = result.Folders[limit-1].ID
		}
		return nil
	})
	return result, err
}

func (h *serverHandler) handleWorkspaceReport(w http.ResponseWriter, r *http.Request, requestID string, actor contracts.Account) {
	if r.Method != http.MethodPut {
		h.methodNotAllowed(w, requestID, http.MethodPut)
		return
	}
	if !h.limiter.allow("workspace.report", actor.AccountID, 60, time.Minute) {
		h.writeError(w, requestID, rateLimitError())
		return
	}
	var input contracts.WorkspaceReport
	if decodeJSONLimit(r, &input, 1024*1024) != nil {
		h.writeError(w, requestID, invalid("invalid_json", "invalid directory snapshot"))
		return
	}
	credential := strings.Fields(r.Header.Get("Authorization"))
	if len(credential) != 2 {
		h.writeError(w, requestID, forbidden("workspace_credential_required", "a device credential is required"))
		return
	}
	result, err := h.authority.reportWorkspaces(actor, hashToken(credential[1]), input)
	h.writeResult(w, requestID, http.StatusOK, result, err)
}

func (h *serverHandler) handleProjectFolders(w http.ResponseWriter, r *http.Request, requestID string, actor contracts.Account, projectID string) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 {
			h.writeError(w, requestID, invalid("invalid_workspace_limit", "invalid directory page size"))
			return
		}
	}
	result, err := h.authority.projectFolders(actor, projectID, r.URL.Query().Get("cursor"), limit)
	h.writeResult(w, requestID, http.StatusOK, result, err)
}
