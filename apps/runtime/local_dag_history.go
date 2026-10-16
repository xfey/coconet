package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/adapters/claude"
	"github.com/xfey/coconet/adapters/codex"
	"github.com/xfey/coconet/adapters/cursor"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/inference"
	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/workgraph"
)

const maxHistoryPlans = 128
const maxHistoryCandidates = 100

type localHistoryRequest struct {
	PlanID     string              `json:"plan_id,omitempty"`
	Agent      contracts.AgentType `json:"agent,omitempty"`
	Root       string              `json:"root,omitempty"`
	ConfigRoot string              `json:"config_root,omitempty"`
	Sessions   []string            `json:"sessions,omitempty"`
	MaxCalls   int                 `json:"max_calls,omitempty"`
	BatchSize  int                 `json:"batch_size,omitempty"`
}

type localHistoryItem struct {
	Session   contracts.DiscoveredSession `json:"session"`
	SessionID string                      `json:"session_id"`
	Status    string                      `json:"status"` // selected, captured, complete, failed
	Source    *workgraph.Source           `json:"source,omitempty"`
	Error     string                      `json:"error,omitempty"`
}

type localHistoryPlan struct {
	Schema        int                           `json:"schema"`
	ID            string                        `json:"id"`
	ProjectID     string                        `json:"project_id"`
	Actor         string                        `json:"actor"`
	Root          string                        `json:"root"`
	ConfigRoot    string                        `json:"config_root"`
	Agent         contracts.AgentType           `json:"agent"`
	CreatedAt     string                        `json:"created_at"`
	Provider      string                        `json:"provider"`
	Model         string                        `json:"model"`
	Configuration string                        `json:"configuration"`
	DailyLimit    int                           `json:"daily_limit,omitempty"`
	Status        string                        `json:"status"`
	Candidates    []contracts.DiscoveredSession `json:"candidates"`
	ScopeNote     string                        `json:"scope_note"`
	Items         []localHistoryItem            `json:"items"`
	MaxCalls      int                           `json:"max_calls,omitempty"`
	UsedCalls     int                           `json:"reserved_calls"`
	BatchSize     int                           `json:"batch_size"`
	BatchThrough  int                           `json:"batch_through"`
}

func historyConfiguration(config localDAGServiceConfig) string {
	return workgraph.Digest([]any{config.Provider, config.Model, config.Command, config.Profile, config.TimeoutSeconds})
}

func historyEngineMatches(engine *localdag.Engine, config localDAGServiceConfig) bool {
	if engine.Provider != config.Provider || engine.Model != config.Model {
		return false
	}
	if described, ok := engine.Runner.(interface{ Configuration() any }); ok {
		if running, ok := described.Configuration().(inference.Config); ok {
			return running.Provider == config.Provider && running.Model == config.Model && running.Command == config.Command && running.ConfigRoot == config.Profile && running.Timeout == time.Duration(config.TimeoutSeconds)*time.Second
		}
	}
	return true
}

func historyLock(ctx context.Context, root string) (func() error, error) {
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	return acquireLocalFileLock(ctx, filepath.Join(root, "history.lock"))
}

func historyPlanPath(root, id string) (string, error) {
	if _, err := hex.DecodeString(id); len(id) != 64 || err != nil || strings.ToLower(id) != id {
		return "", errors.New("invalid history plan ID")
	}
	return filepath.Join(root, "history", id+".json"), nil
}

func readHistoryPlans(root string) ([]localHistoryPlan, error) {
	info, err := os.Lstat(filepath.Join(root, "history"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("history archive must be a real private directory")
	}
	file, err := os.Open(filepath.Join(root, "history"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	entries, err := file.ReadDir(maxHistoryPlans*2 + 1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > maxHistoryPlans*2 {
		return nil, errors.New("history plan directory exceeds capacity")
	}
	result := []localHistoryPlan{}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, errors.New("history archive contains a symbolic link")
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path, err := historyPlanPath(root, strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		data, err := readLocalPrivateFile(path, 512<<10, true)
		if err != nil {
			return nil, err
		}
		var plan localHistoryPlan
		if err := decodeStrict(data, &plan); err != nil {
			return nil, err
		}
		if plan.Schema != 1 || plan.ID+".json" != entry.Name() || plan.ProjectID == "" || plan.Actor == "" || len(plan.Candidates) > maxHistoryCandidates || len(plan.Items) > 50 || plan.UsedCalls < 0 || plan.MaxCalls < 0 || (plan.MaxCalls > 0 && plan.MaxCalls < plan.UsedCalls) || plan.MaxCalls > 10000 || plan.BatchThrough < 0 || plan.BatchThrough > len(plan.Items) || !slices.Contains([]string{"preview", "running", "paused", "failed", "configuration_wait", "budget_wait", "batch_wait", "complete"}, plan.Status) {
			return nil, errors.New("invalid history plan")
		}
		if plan.Status != "preview" && (len(plan.Items) == 0 || plan.BatchSize < 1 || plan.BatchSize > 20 || plan.BatchThrough < 1) {
			return nil, errors.New("invalid active history batch")
		}
		seen := map[string]bool{}
		for _, item := range plan.Items {
			id := localdag.ObservationSessionID(plan.ProjectID, plan.Actor, string(plan.Agent), item.Session.NativeSessionID)
			if item.SessionID != id || seen[id] || !slices.Contains([]string{"selected", "captured", "complete", "failed"}, item.Status) || item.Source != nil && (item.Source.SessionID != id || item.Source.Version != 1) || (item.Status == "captured" || item.Status == "complete") && item.Source == nil {
				return nil, errors.New("invalid imported session identity or phase")
			}
			seen[id] = true
		}
		result = append(result, plan)
	}
	if len(result) > maxHistoryPlans {
		return nil, errors.New("history plan capacity exceeded")
	}
	return result, nil
}

func saveHistoryPlan(root string, plan localHistoryPlan) error {
	path, err := historyPlanPath(root, plan.ID)
	if err != nil {
		return err
	}
	// Match atomicWriteJSON's on-disk encoding, including its final newline.
	// A plan accepted here must remain readable under the same size bound.
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	if len(data)+1 > 512<<10 {
		return errors.New("history plan exceeds its metadata limit; narrow the selection")
	}
	if err := atomicWriteJSON(path, plan, 0600); err != nil {
		return err
	}
	return syncLocalDirectory(filepath.Dir(path))
}

func historyItemEvent(root string, plan localHistoryPlan, item localHistoryItem) (string, localDAGInboxEvent, error) {
	id := workgraph.Digest([]string{plan.ID, item.SessionID})
	path := filepath.Join(root, "history", plan.ID, id+".json")
	if info, err := os.Lstat(filepath.Dir(path)); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return "", localDAGInboxEvent{}, errors.New("history event directory must not be a symbolic link")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", localDAGInboxEvent{}, err
	}
	event, err := readLocalDAGInbox(path)
	if errors.Is(err, os.ErrNotExist) {
		event = localDAGInboxEvent{Schema: 1, ID: id, Request: localDAGObservationRequest{ProjectID: plan.ProjectID, Agent: plan.Agent, NativeID: item.Session.NativeSessionID, Root: plan.Root, CWD: item.Session.CWD, ConfigRoot: plan.ConfigRoot}}
		err = nil
	}
	if err == nil && (event.Request.ProjectID != plan.ProjectID || event.Request.Agent != plan.Agent || event.Request.NativeID != item.Session.NativeSessionID || !samePath(event.Request.Root, plan.Root) || !samePath(event.Request.ConfigRoot, plan.ConfigRoot) || !samePath(event.Request.CWD, item.Session.CWD)) {
		err = errors.New("history capture event differs from its fixed selection")
	}
	return path, event, err
}

func historyOwnsSession(plan localHistoryPlan, session string) bool {
	return slices.ContainsFunc(plan.Items, func(item localHistoryItem) bool { return item.SessionID == session && item.Status != "complete" })
}

func localHistoryBlocksCapture(ctx context.Context, root string, engine *localdag.Engine, request localDAGObservationRequest) (bool, error) {
	unlock, err := historyLock(ctx, root)
	if err != nil {
		return false, err
	}
	defer unlock()
	plans, err := readHistoryPlans(root)
	if err != nil {
		return false, err
	}
	session := localdag.ObservationSessionID(request.ProjectID, engine.Actor, string(request.Agent), request.NativeID)
	for _, plan := range plans {
		if plan.ProjectID == request.ProjectID && historyOwnsSession(plan, session) {
			return true, nil
		}
	}
	return false, nil
}

func historyAdapter(agent contracts.AgentType, profile string) (adapters.Adapter, error) {
	for _, candidate := range runtimeSessionAdapters() {
		switch source := candidate.(type) {
		case codex.Adapter:
			if agent == contracts.AgentCodex {
				source.ConfigRoot = profile
				return source, nil
			}
		case cursor.Adapter:
			if agent == contracts.AgentCursor {
				source.ConfigRoot = profile
				return source, nil
			}
		case claude.Adapter:
			if agent == contracts.AgentClaude {
				source.ConfigRoot = profile
				return source, nil
			}
		}
	}
	return nil, errors.New("choose a supported history Agent")
}

func localHistoryAction(ctx context.Context, root string, engine *localdag.Engine, project, action string, request localHistoryRequest, adapter adapters.Adapter) (any, error) {
	// Capture, selection and cache collection share this order. Selection cannot
	// race an ordinary Hook into publishing the same native Session first.
	unlockInbox, err := acquireLocalFileLock(ctx, filepath.Join(root, "inbox-processing.lock"))
	if err != nil {
		return nil, err
	}
	defer unlockInbox()
	unlock, err := historyLock(ctx, root)
	if err != nil {
		return nil, err
	}
	defer unlock()
	plans, err := readHistoryPlans(root)
	if err != nil {
		return nil, err
	}
	if action == "history-list" {
		result := []localHistoryPlan{}
		for _, plan := range plans {
			if plan.ProjectID == project {
				plan.ScopeNote = fmt.Sprintf("%d candidates; %d selected sessions. Use history status --plan ID for details.", len(plan.Candidates), len(plan.Items))
				plan.Candidates, plan.Items = nil, nil
				result = append(result, plan)
			}
		}
		return result, nil
	}
	if action == "history-preview" {
		config, err := loadLocalDAGService(root)
		if err != nil {
			return nil, err
		}
		if len(plans) >= maxHistoryPlans {
			return nil, errors.New("history plan capacity reached; forget completed plans or unused previews")
		}
		if config.Provider == "" || config.Model == "" {
			return nil, errors.New("configure a local provider and model before previewing history")
		}
		workspace, pathErr := canonicalPath(request.Root)
		if pathErr != nil || !slices.ContainsFunc(config.Workspaces, func(w localDAGWorkspace) bool { return w.ProjectID == project && samePath(w.Path, workspace) }) {
			return nil, errors.New("history workspace is not connected to this project")
		}
		profile, err := canonicalPath(request.ConfigRoot)
		if err != nil {
			return nil, err
		}
		if adapter == nil {
			adapter, err = historyAdapter(request.Agent, profile)
		}
		if err != nil {
			return nil, err
		}
		if adapter.Type() != request.Agent {
			return nil, errors.New("history Agent differs")
		}
		discovery, cancel := context.WithTimeout(ctx, 20*time.Second)
		candidates, err := adapter.Discover(discovery, workspace)
		cancel()
		partial := errors.Is(err, adapters.ErrDiscoveryLimit)
		if err != nil && !partial {
			return nil, err
		}
		state, err := engine.Store.State(project)
		if err != nil {
			return nil, err
		}
		available := []contracts.DiscoveredSession{}
		pending, err := engine.Store.Pending(project)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, candidate := range candidates {
			if candidate.AgentType != request.Agent || !adapters.ValidNativeSessionID(candidate.NativeSessionID) || !pathContains(workspace, candidate.CWD) || seen[candidate.NativeSessionID] {
				continue
			}
			seen[candidate.NativeSessionID] = true
			id := localdag.ObservationSessionID(project, engine.Actor, string(request.Agent), candidate.NativeSessionID)
			if state.Sessions[id].Latest != 0 || slices.ContainsFunc(pending, func(c workgraph.Commit) bool { return c.Snapshot != nil && c.Snapshot.SessionID == id }) || slices.ContainsFunc(plans, func(p localHistoryPlan) bool { return p.ProjectID == project && historyOwnsSession(p, id) }) {
				continue
			}
			available = append(available, candidate)
		}
		count := len(available)
		if len(available) > maxHistoryCandidates {
			available = available[:maxHistoryCandidates]
		}
		plan := localHistoryPlan{Schema: 1, ID: workgraph.Digest([]string{project, engine.Actor, newRequestID()}), ProjectID: project, Actor: engine.Actor, Root: workspace, ConfigRoot: profile, Agent: request.Agent, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Provider: config.Provider, Model: config.Model, Configuration: historyConfiguration(config), DailyLimit: config.MaxCalls, Status: "preview", Candidates: available, Items: []localHistoryItem{}, ScopeNote: fmt.Sprintf("Metadata only; showing %d of %d discovered, not-yet-captured sessions. No model call or transcript read. Start selects at most 50; each is captured once at processing time, without reconstructing past versions. Selected conversations use their source Agent and its default model.", len(available), count)}
		if partial {
			plan.ScopeNote += " Native discovery reached its limit; this is a partial inventory."
		}
		if err := saveHistoryPlan(root, plan); err != nil {
			return nil, err
		}
		return plan, nil
	}
	index := slices.IndexFunc(plans, func(p localHistoryPlan) bool {
		return p.ID == request.PlanID && p.ProjectID == project && p.Actor == engine.Actor
	})
	if index < 0 {
		return nil, errors.New("history plan not found for this project and device")
	}
	plan := plans[index]
	if action == "history-status" {
		return plan, nil
	}
	if action == "history-start" && plan.Status != "preview" {
		selected := []string{}
		for _, item := range plan.Items {
			selected = append(selected, item.Session.NativeSessionID)
		}
		if slices.Equal(selected, request.Sessions) && plan.MaxCalls == request.MaxCalls && plan.BatchSize == request.BatchSize {
			return plan, nil
		}
		return nil, errors.New("history plan already started with a different selection")
	}
	if action == "history-forget" {
		if plan.Status != "preview" && plan.Status != "complete" {
			return nil, errors.New("only completed plans and unused previews can be forgotten")
		}
		// Completed event files contain metadata/digests only. Remove exact known
		// files, never an arbitrary recursively supplied directory.
		for _, item := range plan.Items {
			path, _, err := historyItemEvent(root, plan, item)
			if err != nil {
				return nil, err
			}
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
		if err := os.Remove(filepath.Join(root, "history", plan.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		path, _ := historyPlanPath(root, plan.ID)
		if err := os.Remove(path); err != nil {
			return nil, err
		}
		return map[string]bool{"forgotten": true}, syncLocalDirectory(filepath.Dir(path))
	}
	if action == "history-pause" {
		if plan.Status == "preview" || plan.Status == "complete" {
			return nil, errors.New("history plan has not started or is already complete")
		}
		plan.Status = "paused"
	} else {
		config, err := loadLocalDAGService(root)
		if err != nil {
			return nil, err
		}
		if plan.Configuration != historyConfiguration(config) || config.PauseReason != "" {
			return nil, errors.New("restore the previewed inference configuration and sign-in before continuing history")
		}
		switch action {
		case "history-start":
			if plan.Status != "preview" || len(request.Sessions) == 0 || len(request.Sessions) > 50 || request.MaxCalls < 0 || request.MaxCalls > 100 || request.BatchSize < 1 || request.BatchSize > 20 {
				return nil, errors.New("start requires an unused preview, 1–50 selected IDs, a batch size of 1–20")
			}
			state, err := engine.Store.State(project)
			if err != nil {
				return nil, err
			}
			seen := map[string]bool{}
			pending, err := engine.Store.Pending(project)
			if err != nil {
				return nil, err
			}
			for _, native := range request.Sessions {
				idx := slices.IndexFunc(plan.Candidates, func(s contracts.DiscoveredSession) bool { return s.NativeSessionID == native })
				if idx < 0 || seen[native] {
					return nil, errors.New("selection contains unknown or duplicate session IDs")
				}
				seen[native] = true
				id := localdag.ObservationSessionID(project, engine.Actor, string(plan.Agent), native)
				if state.Sessions[id].Latest != 0 || slices.ContainsFunc(pending, func(c workgraph.Commit) bool { return c.Snapshot != nil && c.Snapshot.SessionID == id }) || slices.ContainsFunc(plans, func(p localHistoryPlan) bool { return p.ProjectID == project && historyOwnsSession(p, id) }) {
					return nil, errors.New("selected session is already captured or belongs to another import; preview again")
				}
				plan.Items = append(plan.Items, localHistoryItem{Session: plan.Candidates[idx], SessionID: id, Status: "selected"})
			}
			plan.MaxCalls, plan.BatchSize = request.MaxCalls, request.BatchSize
			plan.BatchThrough = min(plan.BatchSize, len(plan.Items))
			plan.Candidates = nil
		case "history-resume", "history-retry":
			if plan.Status == "preview" || plan.Status == "complete" {
				return nil, errors.New("plan cannot be resumed")
			}
			if request.MaxCalls != 0 {
				if request.MaxCalls < plan.MaxCalls || request.MaxCalls > 10000 {
					return nil, errors.New("new total call cap must be at least the existing cap and at most 10000; the daily cap still applies")
				}
				plan.MaxCalls = request.MaxCalls
			}
			if plan.MaxCalls > 0 && plan.UsedCalls >= plan.MaxCalls {
				return nil, errors.New("history call cap reached; explicitly increase the total --max-calls to continue")
			}
			if !slices.ContainsFunc(plan.Items[:plan.BatchThrough], func(item localHistoryItem) bool { return item.Status != "complete" }) {
				plan.BatchThrough = min(plan.BatchThrough+plan.BatchSize, len(plan.Items))
			}
			if action == "history-retry" {
				for i := range plan.Items {
					item := &plan.Items[i]
					if item.Status != "failed" {
						continue
					}
					item.Status, item.Error = "selected", ""
					if item.Source != nil {
						item.Status = "captured"
					}
				}
				tasks, err := engine.Store.Tasks(project)
				if err != nil {
					return nil, err
				}
				state, err := engine.Store.State(project)
				if err != nil {
					return nil, err
				}
				for _, task := range tasks {
					if task.Status == "paused" && historyOwnsSession(plan, historyTaskSession(task, state)) {
						if _, err := engine.Retry(project, task.ID); err != nil {
							return nil, err
						}
					}
				}
			} else if slices.ContainsFunc(plan.Items, func(item localHistoryItem) bool { return item.Status == "failed" }) {
				return nil, errors.New("use history retry to resume failed items with their fixed input")
			}
		default:
			return nil, errors.New("unknown history action")
		}
		plan.Status = "running"
	}
	if err := saveHistoryPlan(root, plan); err != nil {
		return nil, err
	}
	return plan, nil
}

func historyTaskSession(task graphstore.Task, state workgraph.State) string {
	if task.Source != nil {
		return task.Source.SessionID
	}
	return state.Nodes[task.NodeID].Source.SessionID
}

type historyCallReservation struct{ Root, Plan, Session string }
type historyCallKey struct{}

func localHistoryTaskContext(root string, engine *localdag.Engine) func(context.Context, graphstore.Task) (context.Context, bool, error) {
	return func(ctx context.Context, task graphstore.Task) (context.Context, bool, error) {
		unlock, err := historyLock(ctx, root)
		if err != nil {
			return ctx, false, err
		}
		defer unlock()
		plans, err := readHistoryPlans(root)
		if err != nil {
			return ctx, false, err
		}
		if len(plans) == 0 {
			return ctx, true, nil
		}
		state, err := engine.Store.State(task.ProjectID)
		if err != nil {
			return ctx, false, err
		}
		session := historyTaskSession(task, state)
		for _, plan := range plans {
			if plan.ProjectID != task.ProjectID || !historyOwnsSession(plan, session) {
				continue
			}
			config, err := loadLocalDAGService(root)
			if err != nil {
				return ctx, false, err
			}
			if plan.Actor != engine.Actor || !historyEngineMatches(engine, config) || plan.Configuration != historyConfiguration(config) || plan.Status != "running" || (plan.MaxCalls > 0 && plan.UsedCalls >= plan.MaxCalls) {
				return ctx, false, nil
			}
			for i, item := range plan.Items {
				if item.SessionID == session {
					if i >= plan.BatchThrough || item.Status != "captured" {
						return ctx, false, nil
					}
					return context.WithValue(localdag.PrivateProcessing(ctx), historyCallKey{}, historyCallReservation{root, plan.ID, session}), true, nil
				}
			}
		}
		return ctx, true, nil
	}
}

// Reserve before invoking a model. A crash conservatively consumes a slot;
// restarting or retrying cannot reset an import's explicitly approved budget.
func reserveHistoryCall(ctx context.Context) error {
	reservation, ok := ctx.Value(historyCallKey{}).(historyCallReservation)
	if !ok {
		return nil
	}
	unlock, err := historyLock(ctx, reservation.Root)
	if err != nil {
		return err
	}
	defer unlock()
	plans, err := readHistoryPlans(reservation.Root)
	if err != nil {
		return err
	}
	for _, plan := range plans {
		if plan.ID != reservation.Plan {
			continue
		}
		if plan.Status != "running" || (plan.MaxCalls > 0 && plan.UsedCalls >= plan.MaxCalls) || !historyOwnsSession(plan, reservation.Session) {
			return &inference.Deferred{Until: time.Now().Add(time.Minute)}
		}
		plan.UsedCalls++
		return saveHistoryPlan(reservation.Root, plan)
	}
	return errors.New("history model reservation lost its plan")
}

// Make one bounded capture per pass. Accepted input, queued model tasks and
// publication receipts survive independently; model work runs on its own loop.
func tickLocalHistory(ctx context.Context, root string, engine *localdag.Engine) error {
	unlockInbox, err := acquireLocalFileLock(ctx, filepath.Join(root, "inbox-processing.lock"))
	if err != nil {
		return err
	}
	defer unlockInbox()
	unlock, err := historyLock(ctx, root)
	if err != nil {
		return err
	}
	defer unlock()
	plans, err := readHistoryPlans(root)
	if err != nil {
		return err
	}
planLoop:
	for _, plan := range plans {
		if plan.Status != "running" && plan.Status != "budget_wait" {
			continue
		}
		before := workgraph.Digest(plan)
		persist := func() error {
			if workgraph.Digest(plan) == before {
				return nil
			}
			return saveHistoryPlan(root, plan)
		}
		config, err := loadLocalDAGService(root)
		if err != nil {
			return err
		}
		if plan.Configuration != historyConfiguration(config) || config.PauseReason != "" {
			plan.Status = "configuration_wait"
			return saveHistoryPlan(root, plan)
		}
		if localDAGIsConnectedRoot(root) && !slices.ContainsFunc(config.Workspaces, func(w localDAGWorkspace) bool { return w.ProjectID == plan.ProjectID && samePath(w.Path, plan.Root) }) {
			plan.Status = "configuration_wait"
			return saveHistoryPlan(root, plan)
		}
		state, err := engine.Store.State(plan.ProjectID)
		if err != nil {
			return err
		}
		tasks, err := engine.Store.Tasks(plan.ProjectID)
		if err != nil {
			return err
		}
		for i := range plan.Items {
			if i >= plan.BatchThrough {
				break
			}
			item := &plan.Items[i]
			if item.Status == "complete" {
				continue
			}
			if item.Status == "captured" {
				for _, task := range tasks {
					if historyTaskSession(task, state) == item.SessionID && task.Status == "paused" {
						item.Status, item.Error, plan.Status = "failed", task.Error, "failed"
						return saveHistoryPlan(root, plan)
					}
				}
				pending := slices.ContainsFunc(tasks, func(t graphstore.Task) bool { return historyTaskSession(t, state) == item.SessionID })
				relations := false
				for _, node := range state.Nodes {
					if node.Source.SessionID == item.SessionID && node.Relations != "ready" {
						relations = true
					}
				}
				if item.Source != nil && state.Sessions[item.SessionID].ProcessedThrough >= item.Source.Version && !pending && !relations {
					item.Status = "complete"
					continue
				}
				if plan.MaxCalls > 0 && plan.UsedCalls >= plan.MaxCalls {
					plan.Status = "budget_wait"
				}
				if err := persist(); err != nil {
					return err
				}
				continue planLoop
			}
			if item.Status == "failed" {
				plan.Status = "failed"
				return saveHistoryPlan(root, plan)
			}
			if plan.MaxCalls > 0 && plan.UsedCalls >= plan.MaxCalls {
				plan.Status = "budget_wait"
				if err := persist(); err != nil {
					return err
				}
				continue planLoop
			}
			path, event, err := historyItemEvent(root, plan, *item)
			if err != nil {
				return err
			}
			if err := atomicWriteJSON(path, event, 0600); err != nil {
				return err
			}
			err = traceLocalDAGInbox(ctx, root, engine, path, &event)
			if err != nil {
				item.Status, item.Error, plan.Status = "failed", err.Error(), "failed"
				if len(item.Error) > 4000 {
					item.Error = item.Error[:4000]
				}
			} else {
				item.Status, item.Source = "captured", event.Source
			}
			return errors.Join(err, saveHistoryPlan(root, plan))
		}
		plan.Status = "complete"
		if plan.BatchThrough < len(plan.Items) {
			plan.Status = "batch_wait"
		}
		if err := saveHistoryPlan(root, plan); err != nil {
			return err
		}
	}
	return nil
}

func traceLocalHistoryAction(ctx context.Context, root string, engine *localdag.Engine, project, action string, request localHistoryRequest) (any, error) {
	if action == "history-list" || action == "history-status" {
		return localHistoryAction(ctx, root, engine, project, action, request, nil)
	}
	input, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	run := graphstore.Run{ID: "history_" + newRequestID(), ProjectID: project, TaskID: action + "/" + request.PlanID, Kind: "history", Status: "running", StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Template: "history/1", Input: input}
	if err := engine.Store.Begin(run); err != nil {
		return nil, err
	}
	value, err := localHistoryAction(ctx, root, engine, project, action, request, nil)
	output, encodeErr := json.Marshal(value)
	if encodeErr != nil {
		return nil, errors.Join(err, encodeErr)
	}
	if archiveErr := engine.Store.SaveResponse(run.ID, string(output), nil); archiveErr != nil {
		return nil, errors.Join(err, archiveErr)
	}
	status, message := "completed", ""
	if err != nil {
		status, message = "failed", err.Error()
	}
	return value, errors.Join(err, engine.Store.Finish(run.ID, status, message, time.Now().UTC().Format(time.RFC3339Nano)))
}

func localHistoryCacheRoots(ctx context.Context, root string, store *graphstore.Store) (graphstore.CacheRoots, error) {
	unlock, err := historyLock(ctx, root)
	if err != nil {
		return graphstore.CacheRoots{}, err
	}
	defer unlock()
	plans, err := readHistoryPlans(root)
	if err != nil {
		return graphstore.CacheRoots{}, err
	}
	roots := graphstore.CacheRoots{}
	for _, plan := range plans {
		for _, item := range plan.Items {
			if item.Status == "complete" {
				continue
			}
			_, event, err := historyItemEvent(root, plan, item)
			if err != nil {
				return roots, err
			}
			if event.Prepared == "" {
				continue
			}
			data, err := store.Object(event.Prepared)
			if err != nil {
				return roots, err
			}
			var observation localdag.Observation
			if err := json.Unmarshal(data, &observation); err != nil {
				return roots, err
			}
			roots.Objects = append(roots.Objects, event.Prepared)
			if observation.ArtifactManifest != "" {
				roots.Artifacts = append(roots.Artifacts, observation.ArtifactManifest)
			}
		}
	}
	return roots, nil
}
