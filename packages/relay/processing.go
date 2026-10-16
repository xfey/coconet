package relay

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
	bolt "go.etcd.io/bbolt"
)

const processingLifetime = 24 * time.Hour
const processingLease = 90 * time.Second
const maxProcessingRecords = 4096
const maxProcessingProjectBytes = 32 << 20
const maxProcessingGlobalBytes = 256 << 20

// Task bodies are temporary bounded capsules, not Session archives. Bodies are
// deleted atomically at completion; indexed expiry also bounds abandoned work.
func (s *Store) RegisterProcessing(project, account string, input contracts.ProcessingRegister) (contracts.ProcessingTask, error) {
	var task contracts.ProcessingTask
	p := input.Spec
	if !validID(project) || !validID(account) || !validID(input.DeviceID) || input.SchemaVersion != 1 || len(p.Key) == 0 || len(p.Key) > 400 || strings.ContainsRune(p.Key, '\x00') || !validID(p.Owner) || !validID(p.SessionID) || p.Version < 1 || (p.Kind != "stage" && p.Kind != "relations") || (p.Kind == "relations" && (!validID(p.NodeID) || p.NodeRevision < 1)) || (p.Kind == "stage" && (p.NodeID != "" || p.NodeRevision != 0)) || len(p.Payload) == 0 || len(p.Payload) > contracts.MaxProcessingInputBytes || contentstore.ObjectDigest(p.Payload) != p.SHA256 {
		return task, ErrInvalid
	}
	descriptor, _ := json.Marshal(p)
	id := "task_" + contentstore.ObjectDigest(descriptor)
	err := s.update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("processing"))
		if data := b.Get(key(project, id)); data != nil {
			if err := json.Unmarshal(data, &task); err != nil {
				return err
			}
			if atTime(task.ExpiresAt).After(s.now()) {
				task.Spec.Payload = bytes.Clone(tx.Bucket([]byte("processing-inputs")).Get(key(project, id)))
				return nil
			}
			// Explicit retry must not wait for the maintenance batch to reach
			// this expired record. Recreate it with fresh fencing and usage.
			if err := deleteProcessing(tx, task); err != nil {
				return err
			}
		}
		usage := tx.Bucket([]byte("processing-usage"))
		if number(usage.Get(key(project, "count"))) >= maxProcessingRecords {
			return ErrQuota
		}
		seq := number(usage.Get(key(project, "sequence"))) + 1
		task = contracts.ProcessingTask{SchemaVersion: 1, ID: id, ProjectID: project, Spec: p, Sequence: seq, Creator: contracts.RelayWriter(account, input.DeviceID), CreatedAt: s.now().UTC().Format(time.RFC3339Nano), ExpiresAt: s.now().Add(processingLifetime).UTC().Format(time.RFC3339Nano), Status: "queued"}
		if err := saveProcessing(tx, task); err != nil {
			return err
		}
		if err := usage.Put(key(project, "sequence"), encodedNumber(seq)); err != nil {
			return err
		}
		if err := usage.Put(key(project, "count"), encodedNumber(number(usage.Get(key(project, "count")))+1)); err != nil {
			return err
		}
		if err := tx.Bucket([]byte("processing-order")).Put(revisionKey(project, seq), []byte(id)); err != nil {
			return err
		}
		return tx.Bucket([]byte("processing-expiry")).Put(dueKey(atTime(task.ExpiresAt).UnixNano(), key(project, id)), []byte{1})
	})
	return task, err
}

func saveProcessing(tx *bolt.Tx, task contracts.ProcessingTask) error {
	b, inputs := tx.Bucket([]byte("processing")), tx.Bucket([]byte("processing-inputs"))
	id := key(task.ProjectID, task.ID)
	payload := inputs.Get(id)
	if task.Spec.Payload != nil {
		payload = task.Spec.Payload
	}
	if task.Status == "completed" || task.Status == "superseded" {
		payload = nil
	}
	task.Spec.Payload = nil
	data, err := json.Marshal(task)
	if err != nil {
		return err
	}
	delta := int64(len(data) + len(payload) - len(b.Get(id)) - len(inputs.Get(id)))
	u := tx.Bucket([]byte("processing-usage"))
	projectBytes, globalBytes := number(u.Get(key(task.ProjectID, "bytes")))+delta, number(u.Get([]byte("global")))+delta
	if projectBytes > maxProcessingProjectBytes || globalBytes > maxProcessingGlobalBytes {
		return ErrQuota
	}
	if err := u.Put(key(task.ProjectID, "bytes"), encodedNumber(projectBytes)); err != nil {
		return err
	}
	if err := u.Put([]byte("global"), encodedNumber(globalBytes)); err != nil {
		return err
	}
	if len(payload) == 0 {
		if err := inputs.Delete(id); err != nil {
			return err
		}
	} else if err := inputs.Put(id, payload); err != nil {
		return err
	}
	return b.Put(id, data)
}

func processingTask(tx *bolt.Tx, project, id string) (contracts.ProcessingTask, error) {
	var t contracts.ProcessingTask
	data := tx.Bucket([]byte("processing")).Get(key(project, id))
	if data == nil {
		return t, contracts.ErrProcessingStale
	}
	err := json.Unmarshal(data, &t)
	return t, err
}

func (s *Store) ProcessingTask(project, id string) (contracts.ProcessingTask, error) {
	var task contracts.ProcessingTask
	if !validID(project) || !validID(id) {
		return task, ErrInvalid
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		task, err = processingTask(tx, project, id)
		if err == nil && !atTime(task.ExpiresAt).After(s.now()) {
			return contracts.ErrProcessingStale
		}
		if err == nil {
			task.Spec.Payload = bytes.Clone(tx.Bucket([]byte("processing-inputs")).Get(key(project, id)))
		}
		return err
	})
	return task, err
}

func (s *Store) ProcessingTasks(project string, after int64) (contracts.ProcessingPage, error) {
	page := contracts.ProcessingPage{SchemaVersion: 1, Tasks: []contracts.ProcessingTask{}}
	if !validID(project) || after < 0 {
		return page, ErrInvalid
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket([]byte("processing-order")).Cursor()
		for k, v := c.Seek(revisionKey(project, after+1)); k != nil && bytes.HasPrefix(k, key(project, "")); k, v = c.Next() {
			t, err := processingTask(tx, project, string(v))
			if err != nil {
				return err
			}
			if !atTime(t.ExpiresAt).After(s.now()) || t.Status == "completed" || t.Status == "superseded" {
				continue
			}
			if len(page.Tasks) == 100 {
				page.Next = page.Tasks[len(page.Tasks)-1].Sequence
				break
			}
			t.Spec.Payload = nil
			// Listing a task never grants its execution capability.
			t.Lease = ""
			page.Tasks = append(page.Tasks, t)
		}
		return nil
	})
	return page, err
}

func (s *Store) ActProcessing(project, account, id string, action contracts.ProcessingAction) (contracts.ProcessingTask, error) {
	var task contracts.ProcessingTask
	if !validID(project) || !validID(account) || !validID(id) || !validID(action.DeviceID) || action.SchemaVersion != 1 || len(action.Reason) > 500 {
		return task, ErrInvalid
	}
	writer := contracts.RelayWriter(account, action.DeviceID)
	err := s.update(func(tx *bolt.Tx) error {
		var err error
		task, err = processingTask(tx, project, id)
		if err != nil {
			return err
		}
		if !atTime(task.ExpiresAt).After(s.now()) || task.Status == "completed" || task.Status == "superseded" {
			return contracts.ErrProcessingStale
		}
		active := task.Status == "running" && atTime(task.ClaimedUntil).After(s.now())
		switch action.Operation {
		case "claim":
			if active {
				if writer == task.Claimant && action.Lease != "" && action.Lease == task.Lease {
					return nil
				}
				return contracts.ErrProcessingBusy
			}
			if task.Status == "paused" || task.Attempts >= 3 {
				return contracts.ErrProcessingPaused
			}
			attemptsByWriter := 0
			for _, actor := range task.AttemptedBy {
				if actor == writer {
					attemptsByWriter++
				}
			}
			if attemptsByWriter >= 2 {
				return contracts.ErrProcessingYielded
			}
			if task.Claimant == writer && atTime(task.RetryAfter).After(s.now()) {
				return contracts.ErrProcessingBusy
			}
			// One Session can have several queued versions but only one live
			// executor. Other Sessions retain independent concurrency.
			c := tx.Bucket([]byte("processing")).Cursor()
			for k, v := c.Seek(key(project, "")); k != nil && bytes.HasPrefix(k, key(project, "")); k, v = c.Next() {
				var other contracts.ProcessingTask
				if err := json.Unmarshal(v, &other); err != nil {
					return err
				}
				if other.ID != id && other.Spec.SessionID == task.Spec.SessionID && other.Status == "running" && atTime(other.ClaimedUntil).After(s.now()) {
					return contracts.ErrProcessingBusy
				}
			}
			var nonce [24]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				return err
			}
			task.Lease = hex.EncodeToString(nonce[:])
			task.Claimant = writer
			task.Attempts++
			task.AttemptedBy = append(task.AttemptedBy, writer)
			task.Status = "running"
			task.Error = ""
			task.RetryAfter = ""
			task.ClaimedUntil = s.now().Add(processingLease).UTC().Format(time.RFC3339Nano)
		case "retry":
			if active {
				return contracts.ErrProcessingBusy
			}
			if task.Attempts == 0 {
				return ErrInvalid
			}
			task.Status = "queued"
			task.Attempts = 0
			task.AttemptedBy = nil
			task.Error = ""
			task.RetryAfter = ""
			task.Lease = ""
		default:
			if action.Operation == "supersede" && task.Spec.Owner == writer {
				task.Status, task.Spec.Payload, task.ClaimedUntil, task.Lease, task.Error = "superseded", nil, "", "", action.Reason
				break
			}
			if !active || task.Claimant != writer || action.Lease == "" || action.Lease != task.Lease {
				return contracts.ErrProcessingStale
			}
			switch action.Operation {
			case "renew":
				task.ClaimedUntil = s.now().Add(processingLease).UTC().Format(time.RFC3339Nano)
			case "fail":
				task.Status = "queued"
				if task.Attempts >= 3 {
					task.Status = "paused"
				}
				task.Error = action.Reason
				task.RetryAfter = s.now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
				task.ClaimedUntil = ""
				task.Lease = ""
			case "defer":
				task.Status = "queued"
				task.Attempts--
				if i := slices.Index(task.AttemptedBy, writer); i >= 0 {
					task.AttemptedBy = append(task.AttemptedBy[:i], task.AttemptedBy[i+1:]...)
				}
				task.RetryAfter = s.now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
				task.ClaimedUntil = ""
				task.Lease = ""
			case "supersede":
				task.Status = "superseded"
				task.Spec.Payload = nil
				task.ClaimedUntil = ""
				task.Lease = ""
				task.Error = action.Reason
			default:
				return ErrInvalid
			}
		}
		return saveProcessing(tx, task)
	})
	return task, err
}

func finishProcessing(tx *bolt.Tx, project, writer string, input contracts.RelayCommit, now time.Time) error {
	if input.Processing == nil {
		return nil
	}
	a := input.Processing
	t, err := processingTask(tx, project, a.TaskID)
	if err != nil {
		return err
	}
	if t.Status != "running" || t.Claimant != writer || !atTime(t.ClaimedUntil).After(now) || !atTime(t.ExpiresAt).After(now) || *a != t.Authorization() {
		return contracts.ErrProcessingStale
	}
	t.Status = "completed"
	t.CommitID = input.ID
	t.Spec.Payload = nil
	t.ClaimedUntil = ""
	t.Error = ""
	return saveProcessing(tx, t)
}

func (s *Store) pruneProcessing(limit int) error {
	return s.reclaim(func(tx *bolt.Tx) error {
		c := tx.Bucket([]byte("processing-expiry")).Cursor()
		for n := 0; n < limit; n++ {
			k, _ := c.First()
			if k == nil || string(k[:20]) > fmt.Sprintf("%020d", s.now().UnixNano()) {
				break
			}
			k = bytes.Clone(k)
			id := bytes.Clone(k[21:])
			b := tx.Bucket([]byte("processing"))
			data := b.Get(id)
			if data != nil {
				var t contracts.ProcessingTask
				if err := json.Unmarshal(data, &t); err != nil {
					return err
				}
				if err := deleteProcessing(tx, t); err != nil {
					return err
				}
			}
			if err := tx.Bucket([]byte("processing-expiry")).Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

func deleteProcessing(tx *bolt.Tx, t contracts.ProcessingTask) error {
	id := key(t.ProjectID, t.ID)
	b, inputs, usage := tx.Bucket([]byte("processing")), tx.Bucket([]byte("processing-inputs")), tx.Bucket([]byte("processing-usage"))
	size := int64(len(b.Get(id)) + len(inputs.Get(id)))
	for _, entry := range []struct {
		key   []byte
		delta int64
	}{{key(t.ProjectID, "bytes"), size}, {[]byte("global"), size}, {key(t.ProjectID, "count"), 1}} {
		if err := usage.Put(entry.key, encodedNumber(max(0, number(usage.Get(entry.key))-entry.delta))); err != nil {
			return err
		}
	}
	if err := tx.Bucket([]byte("processing-order")).Delete(revisionKey(t.ProjectID, t.Sequence)); err != nil {
		return err
	}
	if err := tx.Bucket([]byte("processing-expiry")).Delete(dueKey(atTime(t.ExpiresAt).UnixNano(), id)); err != nil {
		return err
	}
	if err := inputs.Delete(id); err != nil {
		return err
	}
	return b.Delete(id)
}
