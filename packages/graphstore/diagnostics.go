package graphstore

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

type DiagnosticPolicy struct {
	Days     int   `json:"days"`
	MaxBytes int64 `json:"max_bytes"`
}
type DiagnosticArchive struct {
	PrunedAt     string `json:"pruned_at"`
	InputSHA256  string `json:"input_sha256"`
	OutputSHA256 string `json:"output_sha256,omitempty"`
	UsageSHA256  string `json:"usage_sha256,omitempty"`
}
type DiagnosticUsage struct {
	Transfers TransferUsage    `json:"transfers"`
	Bytes     int64            `json:"bytes"`
	Reserved  int64            `json:"reserved"`
	Runs      int              `json:"runs"`
	Pruned    int              `json:"pruned"`
	Pinned    int              `json:"pinned"`
	Policy    DiagnosticPolicy `json:"policy"`
}

func diagnosticPolicy(tx *bolt.Tx) (DiagnosticPolicy, error) {
	policy := DiagnosticPolicy{Days: 7, MaxBytes: 256 << 20}
	if data := tx.Bucket([]byte("settings")).Get([]byte("diagnostic-policy")); data != nil {
		if err := json.Unmarshal(data, &policy); err != nil {
			return policy, err
		}
	}
	if policy.Days < 1 || policy.Days > 365 || policy.MaxBytes < 32<<20 || policy.MaxBytes > 16<<30 {
		return policy, errors.New("invalid diagnostic retention policy")
	}
	return policy, nil
}
func (s *Store) SetDiagnosticPolicy(policy DiagnosticPolicy) error {
	if policy.Days < 1 || policy.Days > 365 || policy.MaxBytes < 32<<20 || policy.MaxBytes > 16<<30 {
		return errors.New("diagnostic retention requires 1–365 days and 32 MiB–16 GiB")
	}
	data, _ := json.Marshal(policy)
	return s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("settings")).Put([]byte("diagnostic-policy"), data) })
}
func diagnosticSize(data []byte) int64 {
	var run struct {
		ArchiveReserved int64 `json:"archive_reserved"`
	}
	_ = json.Unmarshal(data, &run)
	return int64(len(data)) + run.ArchiveReserved
}
func readDiagnosticBytes(tx *bolt.Tx) (int64, error) {
	var value int64
	if data := tx.Bucket([]byte("settings")).Get([]byte("diagnostic-bytes")); data != nil {
		err := json.Unmarshal(data, &value)
		return value, err
	}
	err := tx.Bucket([]byte("runs")).ForEach(func(_, data []byte) error { value += diagnosticSize(data); return nil })
	return value, err
}
func putDiagnosticBytes(tx *bolt.Tx, value int64) error {
	data, _ := json.Marshal(value)
	return tx.Bucket([]byte("settings")).Put([]byte("diagnostic-bytes"), data)
}
func putRun(tx *bolt.Tx, run Run) error {
	// Detailed archives are optional. Transaction identity, status and source
	// roots are not: they remain durable even when the diagnostic budget is full.
	run.ArchiveReserved = 0
	if run.Archive != nil {
		pruneRunContent(&run, time.Now())
	}
	data, err := json.Marshal(run)
	if err != nil {
		return err
	}
	bucket := tx.Bucket([]byte("runs"))
	old := bucket.Get([]byte(run.ID))
	used, err := readDiagnosticBytes(tx)
	if err != nil {
		return err
	}
	next := used - diagnosticSize(old) + int64(len(data)) + run.ArchiveReserved
	if next > used {
		policy, err := diagnosticPolicy(tx)
		if err != nil {
			return err
		}
		if next > policy.MaxBytes {
			if err := pruneDiagnostics(tx, time.Now(), int64(len(data))-diagnosticSize(old)); err != nil {
				return err
			}
			used, err = readDiagnosticBytes(tx)
			if err != nil {
				return err
			}
			old = bucket.Get([]byte(run.ID))
			next = used - diagnosticSize(old) + int64(len(data))
			if next > policy.MaxBytes && !run.Pinned {
				pruneRunContent(&run, time.Now())
				data, err = json.Marshal(run)
				if err != nil {
					return err
				}
				next = used - diagnosticSize(old) + int64(len(data))
			}
		}
	}
	if err := bucket.Put([]byte(run.ID), data); err != nil {
		return err
	}
	return putDiagnosticBytes(tx, next)
}

func pruneRunContent(run *Run, now time.Time) {
	// Small normalized counters survive body retention, including old runs
	// whose raw provider receipt has not yet been normalized.
	retainModelUsage(run)
	if run.Archive == nil {
		run.Archive = &DiagnosticArchive{PrunedAt: now.UTC().Format(time.RFC3339Nano), InputSHA256: ObjectDigest(run.Input)}
	}
	if run.Output != "" {
		run.Archive.OutputSHA256 = ObjectDigest([]byte(run.Output))
	}
	if len(run.Usage) > 0 {
		run.Archive.UsageSHA256 = ObjectDigest(run.Usage)
	}
	run.Input, run.Output, run.Usage = json.RawMessage(`null`), "", nil
	run.ArchiveReserved = 0
}
func (s *Store) PinRun(project, id string, pinned bool) error {
	return s.updateRun(id, func(run *Run) error {
		if run.ProjectID != project {
			return errors.New("processing run belongs to another project")
		}
		if run.Archive != nil && pinned {
			return errors.New("processing content was already pruned; only its audit metadata remains")
		}
		run.Pinned = pinned
		return nil
	})
}

var ErrRunUnavailable = errors.New("processing run is unavailable")

func (s *Store) Run(project, id string) (Run, error) {
	var run Run
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket([]byte("runs")).Get([]byte(id))
		if data == nil {
			return ErrRunUnavailable
		}
		if err := json.Unmarshal(data, &run); err != nil {
			return err
		}
		if run.ProjectID != project {
			return errors.New("processing run belongs to another project")
		}
		return nil
	})
	return run, err
}
func (s *Store) DiagnosticUsage() (DiagnosticUsage, error) {
	var usage DiagnosticUsage
	err := s.db.View(func(tx *bolt.Tx) error {
		policy, err := diagnosticPolicy(tx)
		if err != nil {
			return err
		}
		usage.Policy = policy
		usage.Transfers, err = readTransferUsage(tx)
		if err != nil {
			return err
		}
		return tx.Bucket([]byte("runs")).ForEach(func(_, data []byte) error {
			var run Run
			if err := json.Unmarshal(data, &run); err != nil {
				return err
			}
			usage.Runs++
			usage.Bytes += int64(len(data))
			usage.Reserved += run.ArchiveReserved
			if run.Archive != nil {
				usage.Pruned++
			}
			if run.Pinned {
				usage.Pinned++
			}
			return nil
		})
	})
	return usage, err
}
func (s *Store) PruneDiagnostics(now time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error { return pruneDiagnostics(tx, now, 0) })
}
func pruneDiagnostics(tx *bolt.Tx, now time.Time, room int64) error {
	policy, err := diagnosticPolicy(tx)
	if err != nil {
		return err
	}
	used, err := readDiagnosticBytes(tx)
	if err != nil {
		return err
	}
	protected := map[string]bool{}
	for _, name := range []string{"tasks", "pending"} {
		if err := tx.Bucket([]byte(name)).ForEach(func(_, data []byte) error {
			var ref struct {
				RunID string `json:"run_id"`
			}
			if err := json.Unmarshal(data, &ref); err != nil {
				return err
			}
			protected[ref.RunID] = true
			return nil
		}); err != nil {
			return err
		}
	}
	type candidate struct {
		run Run
		at  time.Time
	}
	candidates := []candidate{}
	if err := tx.Bucket([]byte("runs")).ForEach(func(_, data []byte) error {
		var run Run
		if err := json.Unmarshal(data, &run); err != nil {
			return err
		}
		if run.Pinned || run.Archive != nil || protected[run.ID] || run.Status == "running" || run.Status == "responded" || run.Status == "ready" {
			return nil
		}
		at, err := time.Parse(time.RFC3339Nano, run.FinishedAt)
		if err != nil {
			return nil
		}
		candidates = append(candidates, candidate{run, at})
		return nil
	}); err != nil {
		return err
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].at.Equal(candidates[j].at) {
			return candidates[i].run.ID < candidates[j].run.ID
		}
		return candidates[i].at.Before(candidates[j].at)
	})
	for _, item := range candidates {
		if now.Sub(item.at) < time.Duration(policy.Days)*24*time.Hour && used+room <= policy.MaxBytes {
			continue
		}
		run := item.run
		pruneRunContent(&run, now)
		run.Protected = nil
		old := tx.Bucket([]byte("runs")).Get([]byte(run.ID))
		data, err := json.Marshal(run)
		if err != nil {
			return err
		}
		used += int64(len(data)) - diagnosticSize(old)
		if err := tx.Bucket([]byte("runs")).Put([]byte(run.ID), data); err != nil {
			return err
		}
	}
	return putDiagnosticBytes(tx, used)
}
