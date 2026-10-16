// Package graphstore persists the accepted DAG and its processing evidence.
package graphstore

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/workgraph"
	bolt "go.etcd.io/bbolt"
)

type Store struct {
	indexMu           sync.Mutex
	indexWriteMu      sync.Mutex
	indexDB           *sql.DB
	indexCursor       map[string]string
	cacheMu           sync.Mutex
	contentPins       int
	objectQuotaMu     sync.Mutex
	objectBytes       atomic.Int64
	objectLimit       atomic.Int64
	materializedMu    sync.Mutex
	materializedBytes atomic.Int64
	materializedLimit atomic.Int64
	root              string
	db                *bolt.DB
	objects           *contentstore.Store
}

type Run struct {
	SharedTaskID    string             `json:"shared_task_id,omitempty"`
	InputSHA256     string             `json:"input_sha256,omitempty"`
	Executor        string             `json:"executor,omitempty"`
	SourceOwner     string             `json:"source_owner,omitempty"`
	ID              string             `json:"id"`
	TaskID          string             `json:"task_id"`
	ProjectID       string             `json:"project_id"`
	Kind            string             `json:"kind"`
	Status          string             `json:"status"`
	StartedAt       string             `json:"started_at"`
	FinishedAt      string             `json:"finished_at,omitempty"`
	Provider        string             `json:"provider,omitempty"`
	Model           string             `json:"model,omitempty"`
	Template        string             `json:"template,omitempty"`
	Input           json.RawMessage    `json:"input"`
	Output          string             `json:"output,omitempty"`
	Usage           json.RawMessage    `json:"usage,omitempty"`
	Error           string             `json:"error,omitempty"`
	Base            int64              `json:"base"`
	Source          *workgraph.Source  `json:"source,omitempty"`
	Protected       []workgraph.Source `json:"protected,omitempty"`
	NodeID          string             `json:"node_id,omitempty"`
	CommitID        string             `json:"commit_id,omitempty"`
	Implementation  BuildProvenance    `json:"implementation"`
	Pinned          bool               `json:"pinned,omitempty"`
	Archive         *DiagnosticArchive `json:"archive,omitempty"`
	ArchiveReserved int64              `json:"archive_reserved,omitempty"`
}

type Task struct {
	SharedID        string            `json:"shared_id,omitempty"`
	ExecutorSession string            `json:"executor_session,omitempty"`
	ID              string            `json:"id"`
	ProjectID       string            `json:"project_id"`
	Kind            string            `json:"kind"`
	Source          *workgraph.Source `json:"source,omitempty"`
	NodeID          string            `json:"node_id,omitempty"`
	NodeRevision    int64             `json:"node_revision,omitempty"`
	Expanded        bool              `json:"expanded,omitempty"`
	Status          string            `json:"status"`
	Attempts        int               `json:"attempts"`
	PublishAttempts int               `json:"publish_attempts,omitempty"`
	NextAt          string            `json:"next_at,omitempty"`
	RunID           string            `json:"run_id,omitempty"`
	Error           string            `json:"error,omitempty"`
	AfterPublish    string            `json:"after_publish,omitempty"`
	ContextBasis    string            `json:"context_basis,omitempty"`
}

type Entry struct {
	Commit workgraph.Commit `json:"commit"`
	Change workgraph.Change `json:"change"`
}

func Open(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("graph store root is required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("graph store root must be a real directory")
	}
	path := filepath.Join(root, "graph.db")
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return nil, errors.New("graph database must be a regular file")
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	objects, err := contentstore.Open(root)
	if err != nil {
		db.Close()
		return nil, err
	}
	// The exclusive bbolt owner is established before reclaiming crash remnants.
	if err := objects.CleanupInterruptedStreams(); err != nil {
		db.Close()
		return nil, err
	}
	if err := contentstore.CleanupPrivateTemps(root, []string{".artifact-", ".native-copy-"}); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{root: root, db: db, objects: objects}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range []string{"states", "commits", "changes", "runs", "tasks", "pending", "proposals", "transfers", "settings", "heads", "baselines", "transfer-order", "replicas", "replica-baselines"} {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return err
			}
		}
		return initializeTransferArchive(tx)
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	if err := s.initializeCache(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return errors.Join(s.closeIndex(), s.db.Close()) }

func readState(tx *bolt.Tx, project string) (workgraph.State, error) {
	state := workgraph.New(project)
	if data := tx.Bucket([]byte("states")).Get([]byte(project)); data != nil {
		if err := json.Unmarshal(data, &state); err != nil {
			return state, err
		}
	}
	return state, nil
}

func (s *Store) State(project string) (workgraph.State, error) {
	var state workgraph.State
	err := s.db.View(func(tx *bolt.Tx) error { var err error; state, err = readState(tx, project); return err })
	return state, err
}

func (s *Store) HasCommit(commit workgraph.Commit) (bool, error) {
	data, err := encode(commit)
	if err != nil {
		return false, err
	}
	accepted := false
	err = s.db.View(func(tx *bolt.Tx) error {
		accepted = bytes.Equal(tx.Bucket([]byte("commits")).Get(key(commit.ProjectID, commit.ID)), data)
		return nil
	})
	return accepted, err
}

func (s *Store) Projects() ([]string, error) {
	projects := []string{}
	err := s.db.View(func(tx *bolt.Tx) error {
		seen := map[string]bool{}
		for _, name := range []string{"states", "tasks", "pending"} {
			if err := tx.Bucket([]byte(name)).ForEach(func(k, _ []byte) error {
				project := bytes.SplitN(k, []byte{0}, 2)[0]
				seen[string(project)] = true
				return nil
			}); err != nil {
				return err
			}
		}
		for project := range seen {
			projects = append(projects, project)
		}
		return nil
	})
	sort.Strings(projects)
	return projects, err
}

func encode(value any) ([]byte, error) { return json.Marshal(value) }
func key(project, id string) []byte    { return []byte(project + "\x00" + id) }

// Apply atomically records accepted state, immutable commit and its graph diff.
// requireRun is true for local proposals, false for already accepted peer data.
func (s *Store) Apply(commit workgraph.Commit, requireRun bool) (workgraph.Change, error) {
	return s.ApplyVerified(commit, requireRun, "")
}

// ApplyVerified checks the expected state digest inside the same transaction.
func (s *Store) ApplyVerified(commit workgraph.Commit, requireRun bool, expected string) (workgraph.Change, error) {
	release := s.PinContent()
	defer release()
	var change workgraph.Change
	err := s.db.Update(func(tx *bolt.Tx) error {
		commits := tx.Bucket([]byte("commits"))
		id := key(commit.ProjectID, commit.ID)
		encoded, err := encode(commit)
		if err != nil {
			return err
		}
		if !requireRun && bytes.Equal(tx.Bucket([]byte("pending")).Get(id), encoded) {
			var local Run
			data := tx.Bucket([]byte("runs")).Get([]byte(commit.RunID))
			if json.Unmarshal(data, &local) == nil && local.ProjectID == commit.ProjectID && local.Status == "ready" {
				requireRun = true
			}
		}
		if previous := commits.Get(id); previous != nil {
			if !bytes.Equal(previous, encoded) {
				return errors.New("commit ID reused with different content")
			}
			if err := json.Unmarshal(tx.Bucket([]byte("changes")).Get(id), &change); err != nil {
				return err
			}
			if expected != "" && change.StateDigest != expected {
				return errors.New("accepted state digest differs")
			}
			if requireRun {
				if err := publishRun(tx, commit); err != nil {
					return err
				}
			}
			return tx.Bucket([]byte("pending")).Delete(id)
		}
		var run Run
		if requireRun {
			data := tx.Bucket([]byte("runs")).Get([]byte(commit.RunID))
			if data == nil {
				return errors.New("local publication requires a durable processing record")
			}
			if err := json.Unmarshal(data, &run); err != nil {
				return err
			}
			if run.ProjectID != commit.ProjectID || run.Status != "ready" {
				return errors.New("processing record is not ready for publication")
			}
		}
		state, err := readState(tx, commit.ProjectID)
		if err != nil {
			return err
		}
		next, diff, err := workgraph.Apply(state, commit)
		if err != nil {
			return err
		}
		change = diff
		if expected != "" && change.StateDigest != expected {
			return errors.New("resulting state digest differs")
		}
		stateData, err := encode(next)
		if err != nil {
			return err
		}
		changeData, err := encode(change)
		if err != nil {
			return err
		}
		if err := tx.Bucket([]byte("states")).Put([]byte(commit.ProjectID), stateData); err != nil {
			return err
		}
		if err := tx.Bucket([]byte("heads")).Put([]byte(commit.ProjectID), encoded); err != nil {
			return err
		}
		if err := commits.Put(id, encoded); err != nil {
			return err
		}
		if err := tx.Bucket([]byte("changes")).Put(id, changeData); err != nil {
			return err
		}
		if requireRun {
			if err := publishRun(tx, commit); err != nil {
				return err
			}
		}
		return tx.Bucket([]byte("pending")).Delete(id)
	})
	return change, err
}

func publishRun(tx *bolt.Tx, commit workgraph.Commit) error {
	data := tx.Bucket([]byte("runs")).Get([]byte(commit.RunID))
	if data == nil {
		return errors.New("local publication requires its durable attempt")
	}
	var run Run
	if err := json.Unmarshal(data, &run); err != nil {
		return err
	}
	if run.ProjectID != commit.ProjectID {
		return errors.New("processing project differs from commit")
	}
	if run.Status == "published" && run.CommitID == commit.ID {
		return nil
	}
	if run.Status != "ready" {
		return errors.New("attempt is not ready for publication")
	}
	run.Status, run.CommitID = "published", commit.ID
	run.ArchiveReserved = 0
	proposal, _ := encode(Proposal{Commit: commit, Status: "published"})
	if err := tx.Bucket([]byte("proposals")).Put(key(commit.ProjectID, commit.ID), proposal); err != nil {
		return err
	}
	tasks := tx.Bucket([]byte("tasks"))
	taskKey := key(commit.ProjectID, run.TaskID)
	if data := tasks.Get(taskKey); data != nil {
		var task Task
		if err := json.Unmarshal(data, &task); err != nil {
			return err
		}
		if task.RunID == run.ID {
			if task.AfterPublish != "" {
				if commit.Relations == nil || commit.Relations.Complete || task.AfterPublish != "queued" && task.AfterPublish != "paused" {
					return errors.New("invalid post-publication relation task")
				}
				task.Status, task.AfterPublish = task.AfterPublish, ""
				task.RunID, task.NextAt, task.SharedID = "", "", ""
				task.PublishAttempts = 0
				task.Error = "additional evidence required"
				data, err := encode(task)
				if err != nil {
					return err
				}
				if err := tasks.Put(taskKey, data); err != nil {
					return err
				}
			} else {
				if err := tasks.Delete(taskKey); err != nil {
					return err
				}
			}
		}
	}
	return putRun(tx, run)
}

func (s *Store) History(project string) ([]Entry, error) {
	entries := []Entry{}
	err := s.db.View(func(tx *bolt.Tx) error {
		prefix := key(project, "")
		cursor := tx.Bucket([]byte("commits")).Cursor()
		for k, v := cursor.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = cursor.Next() {
			var e Entry
			if err := json.Unmarshal(v, &e.Commit); err != nil {
				return err
			}
			if err := json.Unmarshal(tx.Bucket([]byte("changes")).Get(k), &e.Change); err != nil {
				return err
			}
			entries = append(entries, e)
		}
		return nil
	})
	// IDs deliberately do not define application order.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Change.After < entries[j].Change.After })
	return entries, err
}

func (s *Store) Begin(run Run) error {
	if run.ID == "" || run.TaskID == "" || run.ProjectID == "" || run.Kind == "" || !json.Valid(run.Input) || len(run.Input) > 2<<20 {
		return errors.New("invalid processing input archive")
	}
	if run.Status != "running" || run.Output != "" || run.CommitID != "" || run.Archive != nil || run.ArchiveReserved != 0 {
		return errors.New("new attempt must start without a result")
	}
	run.Implementation = currentBuildProvenance()
	run.ArchiveReserved = diagnosticResponseReserve
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("runs"))
		if bucket.Get([]byte(run.ID)) != nil {
			return errors.New("attempt ID already exists")
		}
		data, err := encode(run)
		if err != nil {
			return err
		}
		used, err := readDiagnosticBytes(tx)
		if err != nil {
			return err
		}
		policy, err := diagnosticPolicy(tx)
		if err != nil {
			return err
		}
		room := int64(len(data)) + run.ArchiveReserved
		if used+room > policy.MaxBytes {
			if err := pruneDiagnostics(tx, time.Now().UTC(), room); err != nil {
				return err
			}
		}
		return putRun(tx, run)
	})
}

// SaveResponse runs before parsing, including malformed JSON and visible errors.
func (s *Store) SaveResponse(id, output string, usage json.RawMessage) error {
	return s.SaveModelResponse(id, output, usage, "")
}

func (s *Store) SaveModelResponse(id, output string, usage json.RawMessage, model string) error {
	if len(output) > 4<<20 || len(usage) > 64<<10 || len(usage) > 0 && !json.Valid(usage) {
		return errors.New("response archive exceeds limits or has invalid usage")
	}
	return s.updateRun(id, func(run *Run) error {
		if run.Status != "running" {
			return errors.New("attempt response cannot be overwritten")
		}
		run.Output = output
		if model != "" {
			run.Model = model
		}
		run.Usage = usage
		run.Status = "responded"
		run.ArchiveReserved = diagnosticFinalReserve
		return nil
	})
}

func (s *Store) Finish(id, status, message, at string) error {
	if status != "ready" && status != "failed" && status != "skipped" && status != "superseded" && status != "interrupted" && status != "needs_context" && status != "captured" && status != "deferred" && status != "completed" {
		return errors.New("invalid processing result status")
	}
	if len(message) > 8000 {
		return errors.New("processing error exceeds limit")
	}
	return s.updateRun(id, func(run *Run) error {
		if run.Status != "running" && run.Status != "responded" && !(run.Status == "ready" && status == "superseded") {
			return errors.New("attempt result is already final")
		}
		if status == "ready" && run.Status != "responded" {
			return errors.New("publication requires a recorded response")
		}
		run.Status = status
		run.Error = message
		run.FinishedAt = at
		if status != "ready" {
			run.ArchiveReserved = 0
		}
		return nil
	})
}

func (s *Store) updateRun(id string, update func(*Run) error) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("runs"))
		data := bucket.Get([]byte(id))
		if data == nil {
			return errors.New("attempt does not exist")
		}
		var run Run
		if err := json.Unmarshal(data, &run); err != nil {
			return err
		}
		if err := update(&run); err != nil {
			return err
		}
		return putRun(tx, run)
	})
}

func (s *Store) Runs(project string) ([]Run, error) {
	runs := []Run{}
	started := map[string]time.Time{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("runs")).ForEach(func(k, v []byte) error {
			var run Run
			if err := json.Unmarshal(v, &run); err != nil {
				return err
			}
			if run.ProjectID == project {
				runs = append(runs, run)
				started[run.ID], _ = time.Parse(time.RFC3339Nano, run.StartedAt)
			}
			return nil
		})
	})
	sort.Slice(runs, func(i, j int) bool {
		if started[runs[i].ID].Equal(started[runs[j].ID]) {
			return runs[i].ID > runs[j].ID
		}
		return started[runs[i].ID].After(started[runs[j].ID])
	})
	return runs, err
}

func (s *Store) SavePending(commit workgraph.Commit) error {
	data, err := encode(commit)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("pending"))
		id := key(commit.ProjectID, commit.ID)
		if previous := bucket.Get(id); previous != nil && !bytes.Equal(previous, data) {
			return errors.New("pending transaction identity collision")
		}
		proposal, _ := encode(Proposal{Commit: commit, Status: "pending"})
		archive := tx.Bucket([]byte("proposals"))
		if archive.Get(id) == nil {
			if err := archive.Put(id, proposal); err != nil {
				return err
			}
		}
		return bucket.Put(id, data)
	})
}

type Proposal struct {
	Commit workgraph.Commit `json:"commit"`
	Status string           `json:"status"`
	Reason string           `json:"reason,omitempty"`
}

// RebasePending preserves the rejected proposal and atomically replaces its
// outbox entry. A new ID is mandatory after confirmed CAS rejection.
func (s *Store) RebasePending(previous, next workgraph.Commit) error {
	if previous.ID == next.ID || previous.ProjectID != next.ProjectID || previous.RunID != next.RunID {
		return errors.New("invalid proposal rebase identity")
	}
	old, err := encode(previous)
	if err != nil {
		return err
	}
	updated, err := encode(next)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		pending, archive := tx.Bucket([]byte("pending")), tx.Bucket([]byte("proposals"))
		oldID, newID := key(previous.ProjectID, previous.ID), key(next.ProjectID, next.ID)
		if !bytes.Equal(pending.Get(oldID), old) || pending.Get(newID) != nil {
			return errors.New("pending proposal changed before rebase")
		}
		for _, proposal := range []Proposal{{Commit: previous, Status: "conflict", Reason: "project CAS rejected; semantic baseline remains valid"}, {Commit: next, Status: "pending"}} {
			data, _ := encode(proposal)
			if err := archive.Put(key(proposal.Commit.ProjectID, proposal.Commit.ID), data); err != nil {
				return err
			}
		}
		if err := pending.Delete(oldID); err != nil {
			return err
		}
		return pending.Put(newID, updated)
	})
}

func (s *Store) Proposals(project string) ([]Proposal, error) {
	result := []Proposal{}
	err := s.db.View(func(tx *bolt.Tx) error {
		prefix := key(project, "")
		cursor := tx.Bucket([]byte("proposals")).Cursor()
		for k, v := cursor.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = cursor.Next() {
			var proposal Proposal
			if err := json.Unmarshal(v, &proposal); err != nil {
				return err
			}
			result = append(result, proposal)
		}
		return nil
	})
	return result, err
}

func (s *Store) Pending(project string) ([]workgraph.Commit, error) {
	commits := []workgraph.Commit{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("pending")).ForEach(func(k, v []byte) error {
			var c workgraph.Commit
			if err := json.Unmarshal(v, &c); err != nil {
				return err
			}
			if c.ProjectID == project {
				commits = append(commits, c)
			}
			return nil
		})
	})
	return commits, err
}

func (s *Store) RemovePending(project, id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		archive := tx.Bucket([]byte("proposals"))
		k := key(project, id)
		if data := archive.Get(k); data != nil {
			var proposal Proposal
			if err := json.Unmarshal(data, &proposal); err != nil {
				return err
			}
			if proposal.Status == "pending" {
				proposal.Status = "superseded"
				proposal.Reason = "proposal invalidated"
				data, _ = encode(proposal)
				if err := archive.Put(k, data); err != nil {
					return err
				}
			}
		}
		return tx.Bucket([]byte("pending")).Delete(k)
	})
}

func (s *Store) PutTask(task Task) error {
	if task.ID == "" || task.ProjectID == "" {
		return errors.New("invalid task")
	}
	data, err := encode(task)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("tasks")).Put(key(task.ProjectID, task.ID), data) })
}

// EnsureTask creates missing work without resetting a concurrent running job.
func (s *Store) EnsureTask(task Task) error {
	if task.ID == "" || task.ProjectID == "" {
		return errors.New("invalid task")
	}
	data, err := encode(task)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("tasks"))
		id := key(task.ProjectID, task.ID)
		if bucket.Get(id) != nil {
			return nil
		}
		return bucket.Put(id, data)
	})
}

func (s *Store) Tasks(project string) ([]Task, error) {
	tasks := []Task{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("tasks")).ForEach(func(k, v []byte) error {
			var task Task
			if err := json.Unmarshal(v, &task); err != nil {
				return err
			}
			if task.ProjectID == project {
				tasks = append(tasks, task)
			}
			return nil
		})
	})
	return tasks, err
}

func (s *Store) DeleteTask(project, id string) error {
	return s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("tasks")).Delete(key(project, id)) })
}

func (s *Store) Replay(project string) (workgraph.State, error) {
	entries, err := s.History(project)
	if err != nil {
		return workgraph.State{}, err
	}
	state, err := s.ReplayBase(project)
	if err != nil {
		return state, err
	}
	baseline := state.Revision
	for _, entry := range entries {
		if entry.Commit.Base+1 <= baseline {
			continue
		}
		next, change, err := workgraph.Apply(state, entry.Commit)
		if err != nil {
			return state, err
		}
		if change.StateDigest != entry.Change.StateDigest {
			return state, fmt.Errorf("replay divergence at revision %d", change.After)
		}
		state = next
	}
	return state, nil
}
