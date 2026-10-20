// Package relay stores opaque project commits and byte objects. It deliberately
// does not import the DAG reducer, inference providers or prompt templates.
package relay

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xfey/coconet/packages/contentstore"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/storageguard"
	bolt "go.etcd.io/bbolt"
)

var ErrConflict = errors.New("relay project revision changed")
var ErrObject = errors.New("relay object is missing")
var ErrInvalid = errors.New("invalid relay request")
var ErrQuota = errors.New("relay project storage quota exceeded")
var ErrLogQuota = errors.New("relay project commit log quota exceeded")

func invalid(message string) error { return fmt.Errorf("%w: %s", ErrInvalid, message) }

var ErrUploading = errors.New("relay object upload is already in progress")

const MaxPayload = 1 << 20
const MaxProjectBytes int64 = 10 << 30
const MaxProjectLogBytes int64 = 256 << 20

type Store struct {
	statsMu          sync.Mutex
	lastMaintenance  MaintenanceStatus
	historyRetention time.Duration
	guard            *storageguard.Guard
	maintenanceMu    sync.Mutex
	transferCapacity int64
	objectMu         sync.Mutex
	active           map[string]int
	deleting         map[string]bool
	now              func() time.Time
	db               *bolt.DB
	objects          *contentstore.Store
	backend          DirectBackend
	contentTTL       time.Duration
	sealing          sync.Map
}
type objectRecord struct {
	MaintenanceAt int64  `json:"maintenance_at,omitempty"`
	Writer        string `json:"writer,omitempty"`
	ExternalKey   string `json:"external_key,omitempty"`
	StagingKey    string `json:"staging_key,omitempty"`
	UploadUntil   string `json:"upload_until,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
	Deleting      string `json:"deleting,omitempty"`
	RetireAfter   string `json:"retire_after,omitempty"`
	Size          int64  `json:"size"`
	Ready         bool   `json:"ready"`
	Lease         string `json:"lease"`
	ReservedAt    string `json:"reserved_at"`
}

func Open(root string) (*Store, error) { return OpenWithOptions(root, Options{}) }

func OpenWithOptions(root string, options Options) (*Store, error) {
	objects, err := contentstore.Open(root)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(root, "relay.db")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("relay database must be a regular file")
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	if err := objects.CleanupInterruptedStreams(); err != nil {
		db.Close()
		return nil, err
	}
	capacity := options.TransferCapacity
	if capacity == 0 {
		capacity = defaultTransferCapacity
	}
	if options.HistoryRetention < 0 || capacity < 1 || options.ContentTTL < 0 || options.ContentTTL > 0 && (options.ContentTTL < time.Hour || options.Backend == nil) {
		db.Close()
		return nil, invalid("invalid temporary storage policy")
	}
	s := &Store{historyRetention: options.HistoryRetention, guard: options.Guard, transferCapacity: capacity, db: db, objects: objects, active: map[string]int{}, deleting: map[string]bool{}, now: time.Now, backend: options.Backend, contentTTL: options.ContentTTL}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range []string{"heads", "entries", "receipts", "objects", "usage", "checkpoints", "generations", "read-leases", "collections", "content-epochs", "log-usage", "backend", "expiry", "garbage", "transfer-usage", "download-guards", "repairs", "repair-expiry", "repair-counts", "checkpoint-heads", "object-references", "history-floors", "history-maintenance", "processing", "processing-inputs", "processing-usage", "processing-order", "processing-expiry"} {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return err
			}
		}
		logs := tx.Bucket([]byte("log-usage"))
		if logs.Stats().KeyN == 0 {
			if err := tx.Bucket([]byte("entries")).ForEach(func(k, v []byte) error {
				project, _, _ := strings.Cut(string(k), "\x00")
				var receipt contracts.RelayReceipt
				if err := json.Unmarshal(v, &receipt); err != nil {
					return err
				}
				amount := int64(len(k) + len(v) + len(key(project, receipt.Commit.ID)) + 8)
				return logs.Put([]byte(project), encodedNumber(number(logs.Get([]byte(project)))+amount))
			}); err != nil {
				return err
			}
		}
		if err := tx.Bucket([]byte("checkpoints")).ForEach(func(k, v []byte) error {
			meta := tx.Bucket([]byte("checkpoint-heads"))
			if meta.Get(k) != nil {
				return nil
			}
			var receipt contracts.RelayCheckpointReceipt
			if err := json.Unmarshal(v, &receipt); err != nil {
				return err
			}
			data, _ := json.Marshal(receipt.Acknowledgement())
			return meta.Put(k, data)
		}); err != nil {
			return err
		}

		// The exclusive database lock means unfinished reservations belonged to
		// a previous process. Their objects were never eligible for a commit.
		objects, usage := tx.Bucket([]byte("objects")), tx.Bucket([]byte("usage"))
		cursor := objects.Cursor()
		for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
			var record objectRecord
			if err := json.Unmarshal(v, &record); err != nil {
				return err
			}
			if record.Ready || record.ExternalKey != "" {
				project, digest, _ := strings.Cut(string(k), "\x00")
				refs := tx.Bucket([]byte("object-references"))
				if record.Ready && record.Deleting == "" && record.ExternalKey == "" {
					if err := refs.Put(key(digest, project), []byte{1}); err != nil {
						return err
					}
				} else if err := refs.Delete(key(digest, project)); err != nil {
					return err
				}
				continue
			}
			project, _, _ := strings.Cut(string(k), "\x00")
			if err := usage.Put([]byte(project), encodedNumber(max(0, number(usage.Get([]byte(project)))-record.Size))); err != nil {
				return err
			}
			if err := cursor.Delete(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	if err := s.bindBackend(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func validID(v string) bool {
	if len(v) < 1 || len(v) > 200 {
		return false
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}
func validDigest(v string) bool {
	data, err := hex.DecodeString(v)
	return err == nil && len(data) == 32 && hex.EncodeToString(data) == v
}
func key(project, id string) []byte { return []byte(project + "\x00" + id) }
func revisionKey(project string, revision int64) []byte {
	return key(project, fmt.Sprintf("%020d", revision))
}
func number(data []byte) int64 {
	if len(data) != 8 {
		return 0
	}
	return int64(binary.BigEndian.Uint64(data))
}
func encodedNumber(value int64) []byte {
	data := make([]byte, 8)
	binary.BigEndian.PutUint64(data, uint64(value))
	return data
}

func (s *Store) PutObject(project, digest string, size int64, body io.Reader) error {
	if s.backend != nil {
		return invalid("use direct object transfer for this backend")
	}
	if !validID(project) || !validDigest(digest) || size < 0 || size > contentstore.MaxObjectBytes {
		return invalid("invalid relay object descriptor")
	}
	s.objectMu.Lock()
	if s.deleting[digest] {
		s.objectMu.Unlock()
		return ErrBusy
	}
	s.active[digest]++
	s.objectMu.Unlock()
	defer s.releaseObject(digest)
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	lease := hex.EncodeToString(nonce[:])
	id := key(project, digest)
	ready := false
	err := s.update(func(tx *bolt.Tx) error {
		objects, usage := tx.Bucket([]byte("objects")), tx.Bucket([]byte("usage"))
		if err := bumpContentEpoch(tx, project); err != nil {
			return err
		}
		if old := objects.Get(id); old != nil {
			var record objectRecord
			if err := json.Unmarshal(old, &record); err != nil {
				return err
			}
			if record.Size != size {
				return invalid("object size differs from identity")
			}
			if !record.Ready || record.Deleting != "" {
				return ErrUploading
			}
			ready = true
			record.RetireAfter = ""
			encoded, _ := json.Marshal(record)
			return objects.Put(id, encoded)
		}
		used := number(usage.Get([]byte(project)))
		if size > MaxProjectBytes-used {
			return ErrQuota
		}
		data, _ := json.Marshal(objectRecord{Size: size, Lease: lease, ReservedAt: time.Now().UTC().Format(time.RFC3339Nano)})
		if err := usage.Put([]byte(project), encodedNumber(used+size)); err != nil {
			return err
		}
		return objects.Put(id, data)
	})
	if err != nil {
		return err
	}
	// No database transaction spans file or network I/O.
	releaseDisk, reserveErr := s.guard.Reserve(size)
	var writeErr error
	if reserveErr != nil {
		writeErr = reserveErr
	} else {
		writeErr = s.objects.PutStream(body, digest, size)
		releaseDisk()
	}
	if ready {
		if writeErr != nil {
			return writeErr
		}
		return s.reclaim(func(tx *bolt.Tx) error { return deleteRepair(tx, id) })
	}
	finishErr := s.reclaim(func(tx *bolt.Tx) error {
		objects, usage := tx.Bucket([]byte("objects")), tx.Bucket([]byte("usage"))
		var record objectRecord
		if err := json.Unmarshal(objects.Get(id), &record); err != nil {
			return err
		}
		if record.Lease != lease || record.Ready {
			return errors.New("upload reservation changed")
		}
		if writeErr != nil {
			if err := usage.Put([]byte(project), encodedNumber(number(usage.Get([]byte(project)))-size)); err != nil {
				return err
			}
			return objects.Delete(id)
		}
		if err := deleteRepair(tx, id); err != nil {
			return err
		}
		if err := tx.Bucket([]byte("object-references")).Put(key(digest, project), []byte{1}); err != nil {
			return err
		}
		record.Ready = true
		record.Lease = ""
		data, _ := json.Marshal(record)
		return objects.Put(id, data)
	})
	return errors.Join(writeErr, finishErr)
}

func (s *Store) Object(project, digest string) (*ObjectRead, error) {
	if s.backend != nil {
		return nil, invalid("use direct object transfer for this backend")
	}
	if !validID(project) || !validDigest(digest) {
		return nil, ErrObject
	}
	s.objectMu.Lock()
	if s.deleting[digest] {
		s.objectMu.Unlock()
		return nil, ErrBusy
	}
	s.active[digest]++
	s.objectMu.Unlock()
	err := s.db.View(func(tx *bolt.Tx) error {
		var record objectRecord
		data := tx.Bucket([]byte("objects")).Get(key(project, digest))
		if data == nil {
			return ErrObject
		}
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		if !record.Ready || record.Deleting != "" {
			return ErrObject
		}
		return nil
	})
	if err != nil {
		s.releaseObject(digest)
		return nil, err
	}
	file, err := s.objects.OpenObject(digest)
	if err != nil {
		s.releaseObject(digest)
		return nil, err
	}
	return &ObjectRead{File: file, release: func() { s.releaseObject(digest) }}, nil
}

type ObjectRead struct {
	*os.File
	once    sync.Once
	release func()
}

func (r *ObjectRead) Close() error {
	var err error
	r.once.Do(func() { err = r.File.Close(); r.release() })
	return err
}
func (s *Store) releaseObject(digest string) {
	s.objectMu.Lock()
	defer s.objectMu.Unlock()
	s.active[digest]--
	if s.active[digest] == 0 {
		delete(s.active, digest)
	}
}

// AppendCheckpoint publishes an already-reduced explicit edit and its cache in
// one transaction. Relay still validates only envelopes and state anchors.
func (s *Store) AppendCheckpoint(project, account string, input contracts.RelayCommit, checkpoint contracts.RelayCheckpoint) (contracts.RelayReceipt, error) {
	if err := validateCheckpoint(project, account, checkpoint); err != nil {
		return contracts.RelayReceipt{}, err
	}
	if checkpoint.CommitID != input.ID || checkpoint.Revision != input.Base+1 || checkpoint.SHA256 != input.StateSHA256 || checkpoint.DeviceID != input.DeviceID {
		return contracts.RelayReceipt{}, invalid("checkpoint differs from proposed commit")
	}
	return s.append(project, account, input, &checkpoint)
}

func (s *Store) Append(project, account string, input contracts.RelayCommit) (contracts.RelayReceipt, error) {
	return s.append(project, account, input, nil)
}

func (s *Store) append(project, account string, input contracts.RelayCommit, checkpoint *contracts.RelayCheckpoint) (contracts.RelayReceipt, error) {
	var receipt contracts.RelayReceipt
	if !validID(project) || !validID(account) || !validID(input.ID) || !validID(input.DeviceID) || input.SchemaVersion != 1 || input.Base < 0 || input.Base >= 9007199254740991 || (input.Codec != "coconet-json-v2" && input.Codec != "coconet-json-v3" && input.Codec != "coconet-json-v4" && input.Codec != "coconet-json-v5" && input.Codec != "coconet-json-v6") || len(input.Payload) == 0 || len(input.Payload) > MaxPayload || contentstore.ObjectDigest(input.Payload) != input.SHA256 || input.Objects == nil || len(input.Objects) > 8192 {
		return receipt, invalid("invalid relay commit envelope")
	}
	if input.Codec != "coconet-json-v4" && input.Codec != "coconet-json-v5" && input.Codec != "coconet-json-v6" && (input.Processing != nil) != (input.Codec == "coconet-json-v3") {
		return receipt, invalid("processing authorization requires matching codec")
	}
	if input.StateSHA256 != "" && !validDigest(input.StateSHA256) {
		return receipt, invalid("invalid resulting state digest")
	}
	seen := map[string]bool{}
	for _, digest := range input.Objects {
		if !validDigest(digest) || seen[digest] {
			return receipt, invalid("invalid or duplicate object reference")
		}
		seen[digest] = true
	}
	input.Objects = append([]string{}, input.Objects...)
	input.Payload = bytes.Clone(input.Payload)
	sort.Strings(input.Objects)
	canonical, err := json.Marshal(input)
	if err != nil {
		return receipt, err
	}
	// Accepted retries are read-only, including when new writes are paused.
	prior, priorErr := s.Receipt(project, input.ID)
	if priorErr == nil {
		if prior.Writer != contracts.RelayWriter(account, input.DeviceID) || prior.CanonicalSHA256 != contentstore.ObjectDigest(canonical) {
			return receipt, invalid("relay transaction identity reused")
		}
		return contracts.RelayReceipt{SchemaVersion: 1, ProjectID: project, Revision: prior.Revision, Writer: prior.Writer, AcceptedAt: prior.AcceptedAt, Commit: input}, nil
	}
	if !errors.Is(priorErr, contracts.ErrRelayReceiptMissing) {
		return receipt, priorErr
	}
	err = s.update(func(tx *bolt.Tx) error {
		receipts, entries, heads := tx.Bucket([]byte("receipts")), tx.Bucket([]byte("entries")), tx.Bucket([]byte("heads"))
		if receipts.Get(key(project, input.ID)) != nil {
			prior, err := receiptEvidence(tx, project, input.ID)
			if err != nil {
				return err
			}
			if prior.Writer != contracts.RelayWriter(account, input.DeviceID) || prior.CanonicalSHA256 != contentstore.ObjectDigest(canonical) {
				return invalid("relay transaction identity reused")
			}
			receipt = contracts.RelayReceipt{SchemaVersion: 1, ProjectID: project, Revision: prior.Revision, Writer: prior.Writer, AcceptedAt: prior.AcceptedAt, Commit: input}
			return nil
		}
		head := number(heads.Get([]byte(project)))
		if head != input.Base {
			return ErrConflict
		}
		if err := finishProcessing(tx, project, contracts.RelayWriter(account, input.DeviceID), input, s.now()); err != nil {
			return err
		}
		// v2 commits declare immutable content identities. Availability is a
		// separate transfer state: a graph remains valid after objects expire.
		receipt = contracts.RelayReceipt{SchemaVersion: 1, ProjectID: project, Revision: head + 1, Writer: contracts.RelayWriter(account, input.DeviceID), AcceptedAt: s.now().UTC().Format(time.RFC3339Nano), Commit: input}
		data, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		logs := tx.Bucket([]byte("log-usage"))
		used := number(logs.Get([]byte(project)))
		amount := int64(len(revisionKey(project, receipt.Revision)) + len(data) + len(key(project, input.ID)) + 8)
		if amount > MaxProjectLogBytes-used {
			return ErrLogQuota
		}
		if err := logs.Put([]byte(project), encodedNumber(used+amount)); err != nil {
			return err
		}
		if err := entries.Put(revisionKey(project, receipt.Revision), data); err != nil {
			return err
		}
		if err := receipts.Put(key(project, input.ID), encodedNumber(receipt.Revision)); err != nil {
			return err
		}
		if err := bumpContentEpoch(tx, project); err != nil {
			return err
		}
		if err := heads.Put([]byte(project), encodedNumber(receipt.Revision)); err != nil {
			return err
		}
		if checkpoint != nil {
			var published contracts.RelayCheckpointReceipt
			return s.putCheckpoint(tx, project, account, *checkpoint, &published)
		}
		return nil
	})
	return receipt, err
}

func (s *Store) Since(project string, after int64, limit int) (contracts.RelayPage, error) {
	page := contracts.RelayPage{SchemaVersion: 1, Entries: []contracts.RelayReceipt{}}
	if !validID(project) || after < 0 || limit < 1 || limit > 100 {
		return page, invalid("invalid relay cursor")
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		page.Head = number(tx.Bucket([]byte("heads")).Get([]byte(project)))
		if after < number(tx.Bucket([]byte("history-floors")).Get([]byte(project))) {
			return contracts.ErrRelayHistoryCompacted
		}
		page.Hints = &contracts.RelaySyncHints{Policy: s.ContentPolicy(), RepairsPending: number(tx.Bucket([]byte("repair-counts")).Get([]byte(project))) > 0}
		if after > page.Head {
			return invalid("relay cursor is ahead of project")
		}
		cursor := tx.Bucket([]byte("entries")).Cursor()
		size := 0
		prefix := key(project, "")
		for k, v := cursor.Seek(revisionKey(project, after+1)); k != nil && bytes.HasPrefix(k, prefix) && len(page.Entries) < limit; k, v = cursor.Next() {
			if len(page.Entries) > 0 && size+len(v) > 4<<20 {
				break
			}
			var entry contracts.RelayReceipt
			if err := json.Unmarshal(v, &entry); err != nil {
				return err
			}
			page.Entries = append(page.Entries, entry)
			size += len(v)
		}
		return nil
	})
	return page, err
}
