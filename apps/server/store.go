package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/relay"
	"github.com/xfey/coconet/packages/storageguard"
	bolt "go.etcd.io/bbolt"
)

const databaseSchemaVersion = 27

var (
	metaBucket         = []byte("meta")
	stateBucket        = []byte("state")
	stateKey           = []byte("current")
	schemaKey          = []byte("schema_version")
	deploymentKey      = []byte("deployment_id")
	artifactBackendKey = []byte("artifact_backend_id")
)

type tokenRecord struct {
	DeviceID   string `json:"device_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	AccountID  string `json:"account_id"`
}

type operationRecord struct {
	EntityID string `json:"entity_id"`
	Action   string `json:"action"`
}

type chunkRecord struct {
	StoredSHA256 string `json:"stored_sha256,omitempty"`
	StoredSize   int64  `json:"stored_size,omitempty"`
	ProjectID    string `json:"project_id"`
	SHA256       string `json:"sha256"`
	RawSize      int64  `json:"raw_size"`
	ObjectKey    string `json:"object_key"`
	VerifiedAt   string `json:"verified_at"`
}

type syncAttempt struct {
	LastPreparedAt string                        `json:"last_prepared_at,omitempty"`
	Expired        bool                          `json:"expired,omitempty"`
	CleanupDone    bool                          `json:"cleanup_done,omitempty"`
	AttemptToken   string                        `json:"attempt_token"`
	ActorID        string                        `json:"actor_id"`
	Request        contracts.PrepareSyncRequest  `json:"request"`
	ServerSession  string                        `json:"server_session_id"`
	Missing        map[string]string             `json:"missing_chunks"`
	Committed      *contracts.CommitSyncResponse `json:"committed,omitempty"`
	CreatedAt      string                        `json:"created_at"`
}

type forkAttempt struct {
	AttemptToken    string                          `json:"attempt_token"`
	ActorID         string                          `json:"actor_id"`
	Request         contracts.PrepareForkRequest    `json:"request"`
	NativeSessionID string                          `json:"native_session_id,omitempty"`
	RelativeCWD     string                          `json:"relative_cwd,omitempty"`
	Completed       *contracts.CompleteForkResponse `json:"completed,omitempty"`
	CreatedAt       string                          `json:"created_at"`
}

type normalizedVersionRecord struct {
	ObservationSHA256 string                        `json:"observation_sha256,omitempty"`
	Content           *conversation.Manifest        `json:"content,omitempty"`
	Source            *conversation.Manifest        `json:"source,omitempty"`
	NativeLineage     *contracts.NativeLineageHints `json:"native_lineage,omitempty"`
	VisibleTextSHA256 string                        `json:"visible_text_sha256,omitempty"`
	VisibleTextDelta  []contracts.VisibleTextTurn   `json:"visible_text_delta,omitempty"`
	Summary           contracts.SessionIndexRecord  `json:"summary"`
	FromCursor        string                        `json:"from_cursor,omitempty"`
	Reset             bool                          `json:"reset"`
	SourceBaseVersion int                           `json:"source_base_version,omitempty"`
	SourceTurns       []contracts.NormalizedTurn    `json:"source_turns"`
	Turns             []contracts.NormalizedTurn    `json:"turns"`
}

type workExtractionRecord struct {
	StageProposed   bool                     `json:"stage_proposed,omitempty"`
	OpenItems       []contracts.WorkOpenItem `json:"open_items"`
	Changes         *openItemChanges         `json:"open_item_changes,omitempty"`
	ProjectID       string                   `json:"project_id"`
	ServerSessionID string                   `json:"server_session_id"`
	Version         int                      `json:"version"`
	Objective       string                   `json:"objective"`
	Summary         string                   `json:"summary,omitempty"`
	Approach        string                   `json:"approach,omitempty"`
	WorkStartedAt   string                   `json:"work_started_at"`
	WorkUpdatedAt   string                   `json:"work_updated_at"`
	GeneratedAt     string                   `json:"generated_at"`
}

type extractionResultRecord struct {
	contracts.HistoryExtractionResult
	BaseVersion int `json:"base_version"`
}

type workNodeRecord struct {
	Evidence        *contracts.WorkEvidence `json:"evidence,omitempty"`
	WorkNodeID      string                  `json:"work_node_id"`
	ProjectID       string                  `json:"project_id"`
	ServerSessionID string                  `json:"server_session_id"`
	SourceVersion   int                     `json:"source_version"`
	Objective       string                  `json:"objective"`
	Summary         string                  `json:"summary,omitempty"`
	Approach        string                  `json:"approach,omitempty"`
	State           contracts.WorkNodeState `json:"state"`
	WorkStartedAt   string                  `json:"work_started_at"`
	WorkUpdatedAt   string                  `json:"work_updated_at"`
	Parents         []string                `json:"parents"`
	CreatedAt       string                  `json:"created_at"`
	UpdatedAt       string                  `json:"updated_at"`
}

type semanticProjectionJob struct {
	ExtractionRetryCount    int                     `json:"extraction_retry_count,omitempty"`
	GraphRetryCount         int                     `json:"graph_retry_count,omitempty"`
	Candidate               *workExtractionRecord   `json:"candidate,omitempty"`
	Result                  *extractionResultRecord `json:"result,omitempty"`
	BaseRevision            int64                   `json:"base_revision,omitempty"`
	ExtractionSuspended     bool                    `json:"extraction_suspended,omitempty"`
	GraphSuspended          bool                    `json:"graph_suspended,omitempty"`
	ExtractionInvalidCount  int                     `json:"extraction_invalid_count,omitempty"`
	GraphInvalidCount       int                     `json:"graph_invalid_count,omitempty"`
	LeaseToken              string                  `json:"-"`
	GraphSHA256             string                  `json:"-"`
	BatchID                 string                  `json:"-"`
	Stage                   string                  `json:"-"` // Selected work only; the durable job owns both stages.
	ExtractionAttempts      int                     `json:"extraction_attempts,omitempty"`
	ExtractionNextAttemptAt string                  `json:"extraction_next_attempt_at,omitempty"`
	GraphAttempts           int                     `json:"graph_attempts,omitempty"`
	GraphNextAttemptAt      string                  `json:"graph_next_attempt_at,omitempty"`
	ProjectID               string                  `json:"project_id"`
	ServerSessionID         string                  `json:"server_session_id"`
	Version                 int                     `json:"version"`
	Attempts                int                     `json:"attempts"`
	NextAttemptAt           string                  `json:"next_attempt_at"`
	LastError               string                  `json:"last_error,omitempty"`
	CreatedAt               string                  `json:"created_at"`
	UpdatedAt               string                  `json:"updated_at"`
}

type databaseState struct {
	ProjectNotes            *recordCollection[contracts.ProjectNotes] `json:"project_notes"`
	ProjectNoteReceipts     *recordCollection[projectNoteReceipt]     `json:"project_note_receipts"`
	SnapshotCandidates      *recordCollection[string]                 `json:"snapshot_candidates"`
	SnapshotLeases          *recordCollection[snapshotLease]          `json:"snapshot_leases"`
	WorkStates              *recordCollection[sessionWorkRecord]      `json:"session_work_states"`
	contents                artifactStore
	ContentObjects          *recordCollection[contentObjectRecord] `json:"content_objects"`
	recordContext           *recordTransaction
	failureSource           recordSource
	failuresLoaded          bool
	ArtifactGarbage         *recordCollection[string]               `json:"artifact_garbage,omitempty"`
	ArtifactVerifications   *recordCollection[artifactVerification] `json:"artifact_verifications,omitempty"`
	DashboardRevisions      *recordCollection[int64]                `json:"dashboard_revisions"`
	pendingDashboardChanges map[string]bool
	dashboardAccessChanged  bool
	Workspaces              *recordCollection[workspaceRecord]           `json:"workspaces"`
	ExternalIdentities      *recordCollection[string]                    `json:"external_identities"`
	Authorizations          *recordCollection[authorizationRecord]       `json:"authorizations"`
	RecentFailures          []projectFailureRecord                       `json:"recent_failures,omitempty"`
	ActivityEvents          *recordCollection[contracts.ProjectActivity] `json:"activity_events"`
	ActivityOrder           *recordCollection[[]string]                  `json:"activity_order"`

	LibraryCheckpoints *recordCollection[libraryCheckpointRecord]          `json:"library_checkpoints"`
	LibraryTaggings    *recordCollection[libraryTaggingRecord]             `json:"library_taggings"`
	LibraryEntries     *recordCollection[contracts.LibraryEntry]           `json:"library_entries"`
	DashboardSessions  *recordCollection[dashboardSessionRecord]           `json:"dashboard_sessions"`
	ProjectionLeases   *recordCollection[projectionLease]                  `json:"projection_leases"`
	HistoryBatches     *recordCollection[historyBatchRecord]               `json:"history_batches"`
	ProjectProjections *recordCollection[contracts.ProjectProjectionState] `json:"project_projections"`
	Accounts           *recordCollection[contracts.Account]                `json:"accounts"`
	Tokens             *recordCollection[tokenRecord]                      `json:"tokens"`
	Projects           *recordCollection[contracts.Project]                `json:"projects"`
	Memberships        *recordCollection[contracts.Membership]             `json:"memberships"`
	JoinRequests       *recordCollection[contracts.JoinRequest]            `json:"join_requests"`
	AuditEvents        *recordCollection[contracts.AuditEvent]             `json:"audit_events"`
	Operations         *recordCollection[operationRecord]                  `json:"operations"`
	Sessions           *recordCollection[contracts.SessionRecord]          `json:"sessions"`
	SessionKeys        *recordCollection[string]                           `json:"session_keys"`
	Snapshots          *recordCollection[contracts.Snapshot]               `json:"snapshots"`
	Chunks             *recordCollection[chunkRecord]                      `json:"artifact_chunks"`
	SyncAttempts       *recordCollection[syncAttempt]                      `json:"sync_attempts"`
	ForkAttempts       *recordCollection[forkAttempt]                      `json:"fork_attempts"`
	legacyIndexes      map[string]normalizedVersionRecord
	WorkNodes          *recordCollection[workNodeRecord]        `json:"work_nodes"`
	ProjectionJobs     *recordCollection[semanticProjectionJob] `json:"semantic_projection_jobs"`

	pendingActivityCheckpoints map[string]bool
	activityMigration          bool
	storageTime                time.Time
}

func newDatabaseState() databaseState {
	return databaseState{
		ProjectNotes:        newRecordCollectionWith(make(map[string]contracts.ProjectNotes)),
		ProjectNoteReceipts: newRecordCollectionWith(make(map[string]projectNoteReceipt)), SnapshotCandidates: newRecordCollectionWith(make(map[string]string)),
		SnapshotLeases:        newRecordCollectionWith(make(map[string]snapshotLease)),
		WorkStates:            newRecordCollectionWith(make(map[string]sessionWorkRecord)),
		ContentObjects:        newRecordCollectionWith(make(map[string]contentObjectRecord)),
		ArtifactGarbage:       newRecordCollectionWith(make(map[string]string)),
		ArtifactVerifications: newRecordCollectionWith(make(map[string]artifactVerification)),
		DashboardRevisions:    newRecordCollectionWith(make(map[string]int64)),
		Workspaces:            newRecordCollectionWith(make(map[string]workspaceRecord)),
		ExternalIdentities:    newRecordCollectionWith(make(map[string]string)),
		Authorizations:        newRecordCollectionWith(make(map[string]authorizationRecord)),
		ActivityEvents:        newRecordCollectionWith(make(map[string]contracts.ProjectActivity)),
		ActivityOrder:         newRecordCollectionWith(make(map[string][]string)),
		LibraryCheckpoints:    newRecordCollectionWith(make(map[string]libraryCheckpointRecord)),
		LibraryTaggings:       newRecordCollectionWith(make(map[string]libraryTaggingRecord)),
		LibraryEntries:        newRecordCollectionWith(make(map[string]contracts.LibraryEntry)),
		DashboardSessions:     newRecordCollectionWith(make(map[string]dashboardSessionRecord)),
		ProjectionLeases:      newRecordCollectionWith(make(map[string]projectionLease)),
		HistoryBatches:        newRecordCollectionWith(make(map[string]historyBatchRecord)),
		ProjectProjections:    newRecordCollectionWith(make(map[string]contracts.ProjectProjectionState)),
		Accounts:              newRecordCollectionWith(make(map[string]contracts.Account)),
		Tokens:                newRecordCollectionWith(make(map[string]tokenRecord)),
		Projects:              newRecordCollectionWith(make(map[string]contracts.Project)),
		Memberships:           newRecordCollectionWith(make(map[string]contracts.Membership)),
		JoinRequests:          newRecordCollectionWith(make(map[string]contracts.JoinRequest)),
		AuditEvents:           newRecordCollectionWith(make(map[string]contracts.AuditEvent)),
		Operations:            newRecordCollectionWith(make(map[string]operationRecord)),
		Sessions:              newRecordCollectionWith(make(map[string]contracts.SessionRecord)),
		SessionKeys:           newRecordCollectionWith(make(map[string]string)),
		Snapshots:             newRecordCollectionWith(make(map[string]contracts.Snapshot)),
		Chunks:                newRecordCollectionWith(make(map[string]chunkRecord)),
		SyncAttempts:          newRecordCollectionWith(make(map[string]syncAttempt)),
		ForkAttempts:          newRecordCollectionWith(make(map[string]forkAttempt)),
		WorkNodes:             newRecordCollectionWith(make(map[string]workNodeRecord)),
		ProjectionJobs:        newRecordCollectionWith(make(map[string]semanticProjectionJob)),
	}
}

type store struct {
	capacity       *storageguard.Guard
	relayMu        sync.Mutex
	relay          *relay.Store
	relayCancel    context.CancelFunc
	relayDone      chan struct{}
	contents       artifactStore
	observeRecords func(recordAccessStats)
	eventCancel    context.CancelFunc
	eventDone      chan struct{}
	events         *dashboardEventHub
	db             *bolt.DB
	postgres       *sql.DB
}

func openStore(path string, backends ...artifactStore) (*store, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	var contents artifactStore
	if len(backends) > 0 && backends[0] != nil {
		contents = backends[0]
	} else {
		contents, err = openFilesystemArtifactStore(path + ".content")
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	result := &store{db: db, contents: contents, events: newDashboardEventHub()}
	if err := result.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := result.migrateAuthorityModel(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return result, nil
}

func (s *store) close() error {
	s.relayMu.Lock()
	var relayErr error
	if s.relay != nil {
		if s.relayCancel != nil {
			s.relayCancel()
			<-s.relayDone
		}
		relayErr = s.relay.Close()
		s.relay = nil
	}
	s.relayMu.Unlock()
	if s.eventCancel != nil {
		s.eventCancel()
		<-s.eventDone
	}
	if s.postgres != nil {
		return errors.Join(relayErr, s.postgres.Close())
	}
	return errors.Join(relayErr, s.db.Close())
}

func openPostgresStore(databaseURL string, backends ...artifactStore) (*store, error) {
	if databaseURL == "" {
		return nil, errors.New("PostgreSQL database URL is required")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL: %w", err)
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect PostgreSQL: %w", err)
	}
	content, err := json.Marshal(newDatabaseState())
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS coconet_state (
			singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
			schema_version INTEGER NOT NULL,
			deployment_id TEXT,
			artifact_backend_id TEXT,
			document JSONB NOT NULL
		)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate PostgreSQL: %w", err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE coconet_state ADD COLUMN IF NOT EXISTS deployment_id TEXT`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate PostgreSQL Deployment identity: %w", err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE coconet_state ADD COLUMN IF NOT EXISTS artifact_backend_id TEXT`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate PostgreSQL artifact backend identity: %w", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO coconet_state (singleton, schema_version, document) VALUES (TRUE, $1, $2::jsonb) ON CONFLICT (singleton) DO NOTHING`, databaseSchemaVersion, content); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize PostgreSQL state: %w", err)
	}
	if err := migratePostgresSchema(ctx, db, backends...); err != nil {
		_ = db.Close()
		return nil, err
	}
	result := &store{postgres: db, events: newDashboardEventHub()}
	if len(backends) > 0 {
		result.contents = backends[0]
	}
	if err := result.migrateAuthorityModel(); err != nil {
		_ = db.Close()
		return nil, err
	}
	result.startDashboardListener(databaseURL)
	return result, nil
}

func (s *store) migrateAuthorityModel() error {
	return s.update(func(state *databaseState) error {
		for key, project := range state.Projects.All() {
			if project.CreatedBy == "" {
				project.CreatedBy = project.OwnerID
				state.Projects.Set(key, project)
			}
		}
		for key, membership := range state.Memberships.All() {
			if membership.Status == contracts.MembershipPending {
				state.Memberships.Delete(key)
				continue
			}
			if membership.Role != contracts.RoleMember {
				membership.Role = contracts.RoleMember
				state.Memberships.Set(key, membership)
			}
		}
		for key, request := range state.JoinRequests.All() {
			if request.Status == contracts.JoinRequestPending {
				request.Status = contracts.JoinRequestRejected
				if request.DecidedAt == "" {
					request.DecidedAt = request.CreatedAt
				}
				state.JoinRequests.Set(key, request)
			}
		}
		return nil
	})
}

func migratePostgresSchema(ctx context.Context, db *sql.DB, backends ...artifactStore) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	var content []byte
	if err := tx.QueryRowContext(ctx, `SELECT schema_version, document FROM coconet_state WHERE singleton = TRUE FOR UPDATE`).Scan(&version, &content); err != nil {
		return err
	}
	if version == databaseSchemaVersion {
		return tx.Commit()
	}
	if err := validateStoredSchema(version); err != nil {
		return err
	}
	if version < 1 || version >= databaseSchemaVersion {
		return fmt.Errorf("unsupported database schema version %d", version)
	}
	state, err := decodeStateJSON(content)
	if err != nil {
		return err
	}
	if len(backends) > 0 {
		state.contents = backends[0]
	}
	if err := migrateDatabaseState(&state, version); err != nil {
		return err
	}
	if err := migrateSnapshotContents(&state); err != nil {
		return err
	}
	updated, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE coconet_state SET schema_version = $1, document = $2::jsonb WHERE singleton = TRUE`, databaseSchemaVersion, updated); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *store) bindArtifactBackend(backendID string) error {
	parts := strings.Split(backendID, ":")
	if len(parts) != 2 || parts[0] != "filesystem" && parts[0] != "s3" {
		return errors.New("artifact backend identity is invalid")
	}
	digest, err := hex.DecodeString(parts[1])
	if err != nil || len(digest) != sha256.Size {
		return errors.New("artifact backend identity is invalid")
	}
	if s.postgres != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := s.postgres.ExecContext(ctx, `UPDATE coconet_state SET artifact_backend_id = $1 WHERE singleton = TRUE AND artifact_backend_id IS NULL`, backendID); err != nil {
			return err
		}
		var stored string
		if err := s.postgres.QueryRowContext(ctx, `SELECT artifact_backend_id FROM coconet_state WHERE singleton = TRUE`).Scan(&stored); err != nil {
			return err
		}
		if stored != backendID {
			return errors.New("metadata database is bound to a different artifact backend; migrate artifacts explicitly before changing it")
		}
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		meta := tx.Bucket(metaBucket)
		stored := meta.Get(artifactBackendKey)
		if stored == nil {
			return meta.Put(artifactBackendKey, []byte(backendID))
		}
		if string(stored) != backendID {
			return errors.New("metadata database is bound to a different artifact backend; migrate artifacts explicitly before changing it")
		}
		return nil
	})
}

func (s *store) artifactBackendID() (string, error) {
	if s.postgres != nil {
		var backendID sql.NullString
		if err := s.postgres.QueryRow(`SELECT artifact_backend_id FROM coconet_state WHERE singleton = TRUE`).Scan(&backendID); err != nil {
			return "", err
		}
		return backendID.String, nil
	}
	var backendID string
	err := s.db.View(func(tx *bolt.Tx) error {
		backendID = string(tx.Bucket(metaBucket).Get(artifactBackendKey))
		return nil
	})
	return backendID, err
}

func (s *store) bindDeployment(deploymentID string) error {
	if !deploymentIDPattern.MatchString(deploymentID) {
		return errors.New("Deployment ID is invalid")
	}
	if s.postgres != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := s.postgres.ExecContext(ctx, `UPDATE coconet_state SET deployment_id = $1 WHERE singleton = TRUE AND deployment_id IS NULL`, deploymentID); err != nil {
			return err
		}
		var stored string
		if err := s.postgres.QueryRowContext(ctx, `SELECT deployment_id FROM coconet_state WHERE singleton = TRUE`).Scan(&stored); err != nil {
			return err
		}
		if stored != deploymentID {
			return errors.New("metadata database belongs to a different Coconet Deployment")
		}
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		meta := tx.Bucket(metaBucket)
		stored := meta.Get(deploymentKey)
		if stored == nil {
			return meta.Put(deploymentKey, []byte(deploymentID))
		}
		if string(stored) != deploymentID {
			return errors.New("metadata database belongs to a different Coconet Deployment")
		}
		return nil
	})
}

func (s *store) deploymentID() (string, error) {
	if s.postgres != nil {
		var deploymentID string
		if err := s.postgres.QueryRow(`SELECT deployment_id FROM coconet_state WHERE singleton = TRUE`).Scan(&deploymentID); err != nil {
			return "", err
		}
		return deploymentID, nil
	}
	var deploymentID string
	err := s.db.View(func(tx *bolt.Tx) error {
		deploymentID = string(tx.Bucket(metaBucket).Get(deploymentKey))
		return nil
	})
	return deploymentID, err
}

func (s *store) migrate() error {
	if err := s.recoverMigrationObjects(); err != nil {
		return err
	}
	backend := s.contents
	if backend != nil {
		s.contents = &migrationContentStore{artifactStore: backend, path: s.db.Path() + ".migration-objects.json", journal: migrationObjectJournal{Backend: backend.BackendIdentity(), Keys: []string{}}}
	}
	err := s.db.Update(func(tx *bolt.Tx) error {
		meta, err := tx.CreateBucketIfNotExists(metaBucket)
		if err != nil {
			return err
		}
		legacy, err := tx.CreateBucketIfNotExists(stateBucket)
		if err != nil {
			return err
		}
		version := meta.Get(schemaKey)
		if version != nil {
			if len(version) != 1 {
				return fmt.Errorf("unsupported database schema version %v", version)
			}
			if err := validateStoredSchema(int(version[0])); err != nil {
				return err
			}
			if int(version[0]) > databaseSchemaVersion {
				return fmt.Errorf("unsupported database schema version %d", version[0])
			}
			if int(version[0]) == databaseSchemaVersion {
				context := &recordTransaction{active: true}
				defer func() { context.active = false }()
				_, _, err := bindRecordState(tx, context)
				return err
			}
		}
		state := newDatabaseState()
		if version != nil {
			var content []byte
			if int(version[0]) >= 23 && legacy.Get(stateKey) == nil {
				content, err = exportRecordJSON(tx)
				if err != nil {
					return err
				}
			} else {
				content = legacy.Get(stateKey)
			}
			if content == nil {
				return errors.New("database state is missing")
			}
			state, err = decodeStateJSON(content)
			if err != nil {
				return err
			}
			state.contents = s.contents
			if err = migrateDatabaseState(&state, int(version[0])); err != nil {
				return err
			}
		}
		state.contents = s.contents
		if err = migrateSnapshotContents(&state); err != nil {
			return err
		}
		if err = writeInitialRecordState(tx, &state); err != nil {
			return err
		}
		if err = legacy.Delete(stateKey); err != nil {
			return err
		}
		return meta.Put(schemaKey, []byte{databaseSchemaVersion})
	})
	s.contents = backend
	return errors.Join(err, s.recoverMigrationObjects())
}

func migrateNormalizedHistory(state *databaseState) error {
	if state.snapshotContents().Len() == 0 {
		return nil
	}
	legacy := make(map[string]normalizedVersionRecord, state.snapshotContents().Len())
	versionsBySession := make(map[string][]int)
	for key, record := range state.snapshotContents().All() {
		legacy[key] = record
		versionsBySession[record.Summary.ServerSessionID] = append(versionsBySession[record.Summary.ServerSessionID], record.Summary.Version)
	}
	for serverSessionID, versions := range versionsBySession {
		sort.Ints(versions)
		history := []contracts.NormalizedTurn{}
		previousVersion := 0
		epochByVersion := make(map[int]int, len(versions))
		for _, version := range versions {
			key := versionKey(serverSessionID, version)
			record := legacy[key]
			legacySourceBase := record.Summary.BaseIndexVersion
			epoch := 1
			if legacySourceBase > 0 {
				epoch = epochByVersion[legacySourceBase]
				if epoch == 0 {
					epoch = 1
				}
			} else if previousVersion > 0 {
				epoch = epochByVersion[previousVersion]
				if epoch == 0 {
					epoch = 1
				}
			}
			if record.Reset && previousVersion > 0 {
				epoch = epochByVersion[previousVersion] + 1
			}
			epochByVersion[version] = epoch

			sourceTurns := append([]contracts.NormalizedTurn(nil), record.Turns...)
			var delta []contracts.NormalizedTurn
			history, delta = mergeCumulativeTurns(history, sourceTurns)
			record.Summary.BaseIndexVersion = previousVersion
			record.Summary.SourceBaseVersion = legacySourceBase
			record.Summary.SourceEpoch = epoch
			record.Summary.TurnCount = len(history)
			hash, err := hashNormalizedTurns(history)
			if err != nil {
				return err
			}
			record.Summary.IndexSHA256 = hash
			record.SourceBaseVersion = legacySourceBase
			record.SourceTurns = sourceTurns
			record.Turns = delta
			state.snapshotContents().Set(key, record)
			previousVersion = version
		}
	}
	return nil
}

func (s *store) view(read func(databaseState) error) error {
	if s.postgres != nil {
		var version int
		var content []byte
		var storageTime time.Time
		if err := s.postgres.QueryRow(`SELECT schema_version, document, clock_timestamp() FROM coconet_state WHERE singleton = TRUE`).Scan(&version, &content, &storageTime); err != nil {
			return err
		}
		if version != databaseSchemaVersion {
			return fmt.Errorf("unsupported database schema version %d", version)
		}
		state, err := decodeStateJSON(content)
		if err != nil {
			return err
		}
		state.storageTime = storageTime
		state.contents = s.contents
		return read(state)
	}
	return s.db.View(func(tx *bolt.Tx) error {
		context := &recordTransaction{active: true}
		defer func() {
			context.active = false
			if s.observeRecords != nil {
				s.observeRecords(context.stats)
			}
		}()
		state, _, err := bindRecordState(tx, context)
		if err != nil {
			return err
		}
		state.contents = s.contents
		err = read(state)
		if context.err != nil {
			return context.err
		}
		return err
	})
}

func (s *store) update(change func(*databaseState) error) error {
	var changed map[string]bool
	var access bool
	if s.postgres != nil {
		ctx := context.Background()
		tx, err := s.postgres.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var version int
		var content []byte
		if err := tx.QueryRowContext(ctx, `SELECT schema_version, document FROM coconet_state WHERE singleton = TRUE FOR UPDATE`).Scan(&version, &content); err != nil {
			return err
		}
		if version != databaseSchemaVersion {
			return fmt.Errorf("unsupported database schema version %d", version)
		}
		state, err := decodeStateJSON(content)
		if err != nil {
			return err
		}
		// Read database time after acquiring the row lock. Worker clock skew
		// must not let an expired lease write after a successor has claimed it.
		if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&state.storageTime); err != nil {
			return err
		}
		state.contents = s.contents
		if err := change(&state); err != nil {
			return err
		}
		if len(state.legacyIndexes) > 0 {
			if err := migrateSnapshotContents(&state); err != nil {
				return err
			}
		}
		finalizeDashboardChanges(&state)
		changed, access = state.pendingDashboardChanges, state.dashboardAccessChanged
		updated, err := json.Marshal(state)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE coconet_state SET document = $1::jsonb WHERE singleton = TRUE`, updated); err != nil {
			return err
		}
		if len(changed) > 0 || access {
			payload, err := json.Marshal(dashboardNotification{Projects: changed, Access: access})
			if err != nil {
				return err
			}
			if len(payload) > 6000 {
				payload = []byte(`{"access":true}`)
			}
			if _, err := tx.ExecContext(ctx, "SELECT pg_notify($1, $2)", dashboardNotifyChannel, string(payload)); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		s.events.publish(changed, access)
		return nil
	}
	err := s.guardedUpdate(func(tx *bolt.Tx) error {
		context := &recordTransaction{active: true}
		defer func() {
			context.active = false
			if s.observeRecords != nil {
				s.observeRecords(context.stats)
			}
		}()
		state, tables, err := bindRecordState(tx, context)
		if err != nil {
			return err
		}
		failureSource := state.failureSource
		state.contents = s.contents
		err = change(&state)
		state.contents = s.contents
		if state.recordContext != context {
			state.recordContext = context
			state.failureSource = failureSource
			state.failuresLoaded = true
		}
		if context.err != nil {
			return context.err
		}
		if err != nil {
			return err
		}
		if len(state.legacyIndexes) > 0 {
			if err := migrateSnapshotContents(&state); err != nil {
				return err
			}
		}
		finalizeDashboardChanges(&state)
		changed, access = state.pendingDashboardChanges, state.dashboardAccessChanged
		return flushRecordState(&state, tables)
	})
	if err == nil {
		s.events.publish(changed, access)
	}
	return err
}

func decodeState(tx *bolt.Tx) (databaseState, error) {
	content := tx.Bucket(stateBucket).Get(stateKey)
	if content == nil {
		return databaseState{}, errors.New("database state is missing")
	}
	return decodeStateJSON(content)
}

func decodeStateJSON(content []byte) (databaseState, error) {
	var state databaseState
	if err := json.Unmarshal(content, &state); err != nil {
		return databaseState{}, fmt.Errorf("decode database state: %w", err)
	}

	var legacy struct {
		DocumentHeads map[string]int64                   `json:"project_document_heads"`
		Indexes       map[string]normalizedVersionRecord `json:"session_indexes"`
		Versions      map[string]contracts.Snapshot      `json:"session_versions"`
	}
	if err := json.Unmarshal(content, &legacy); err != nil {
		return databaseState{}, err
	}
	state.legacyIndexes = legacy.Indexes
	if state.Snapshots == nil && legacy.Versions != nil {
		state.Snapshots = newRecordCollectionWith(legacy.Versions)
	}
	normalizeDatabaseState(&state)
	// Pre-release shared documents are intentionally discarded. Advance the
	// project version so already installed Runtimes replace their old cache.
	for project, head := range legacy.DocumentHeads {
		if !state.ProjectNotes.Has(project) {
			state.ProjectNotes.Set(project, contracts.ProjectNotes{SchemaVersion: 1, ProjectID: project, Version: head + 1, Notes: []contracts.ProjectNote{}})
		}
	}
	if err := migrateWorkRecords(&state, content); err != nil {
		return databaseState{}, err
	}
	return state, nil
}

func normalizeDatabaseState(state *databaseState) {
	if state.ProjectNotes == nil {
		state.ProjectNotes = newRecordCollectionWith(make(map[string]contracts.ProjectNotes))
	}
	if state.ProjectNoteReceipts == nil {
		state.ProjectNoteReceipts = newRecordCollectionWith(make(map[string]projectNoteReceipt))
	}
	if state.SnapshotCandidates == nil {
		state.SnapshotCandidates = newRecordCollectionWith(make(map[string]string))
	}
	if state.SnapshotLeases == nil {
		state.SnapshotLeases = newRecordCollectionWith(make(map[string]snapshotLease))
	}
	if state.WorkStates == nil {
		state.WorkStates = newRecordCollectionWith(make(map[string]sessionWorkRecord))
	}
	if state.ContentObjects == nil {
		state.ContentObjects = newRecordCollectionWith(make(map[string]contentObjectRecord))
	}
	if state.ArtifactGarbage == nil {
		state.ArtifactGarbage = newRecordCollectionWith(make(map[string]string))
	}
	if state.ArtifactVerifications == nil {
		state.ArtifactVerifications = newRecordCollectionWith(make(map[string]artifactVerification))
	}
	if state.DashboardRevisions == nil {
		state.DashboardRevisions = newRecordCollectionWith(make(map[string]int64))
	}
	if state.ActivityEvents == nil {
		state.ActivityEvents = newRecordCollectionWith(make(map[string]contracts.ProjectActivity))
	}
	if state.ActivityOrder == nil {
		state.ActivityOrder = newRecordCollectionWith(make(map[string][]string))
	}
	if state.LibraryCheckpoints == nil {
		state.LibraryCheckpoints = newRecordCollectionWith(make(map[string]libraryCheckpointRecord))
	}
	if state.LibraryTaggings == nil {
		state.LibraryTaggings = newRecordCollectionWith(make(map[string]libraryTaggingRecord))
	}
	if state.DashboardSessions == nil {
		state.DashboardSessions = newRecordCollectionWith(make(map[string]dashboardSessionRecord))
	}
	if state.ProjectionLeases == nil {
		state.ProjectionLeases = newRecordCollectionWith(make(map[string]projectionLease))
	}
	if state.Workspaces == nil {
		state.Workspaces = newRecordCollectionWith(make(map[string]workspaceRecord))
	}
	defaults := newDatabaseState()
	if state.HistoryBatches == nil {
		state.HistoryBatches = newRecordCollectionWith(defaults.HistoryBatches.All())
	}
	if state.ProjectProjections == nil {
		state.ProjectProjections = newRecordCollectionWith(defaults.ProjectProjections.All())
	}
	if state.ExternalIdentities == nil {
		state.ExternalIdentities = newRecordCollectionWith(make(map[string]string))
	}
	if state.Authorizations == nil {
		state.Authorizations = newRecordCollectionWith(make(map[string]authorizationRecord))
	}
	if state.Accounts == nil {
		state.Accounts = newRecordCollectionWith(defaults.Accounts.All())
	}
	if state.Tokens == nil {
		state.Tokens = newRecordCollectionWith(defaults.Tokens.All())
	}
	if state.Projects == nil {
		state.Projects = newRecordCollectionWith(defaults.Projects.All())
	}
	if state.Memberships == nil {
		state.Memberships = newRecordCollectionWith(defaults.Memberships.All())
	}
	if state.JoinRequests == nil {
		state.JoinRequests = newRecordCollectionWith(defaults.JoinRequests.All())
	}
	if state.AuditEvents == nil {
		state.AuditEvents = newRecordCollectionWith(defaults.AuditEvents.All())
	}
	if state.Operations == nil {
		state.Operations = newRecordCollectionWith(defaults.Operations.All())
	}
	if state.Sessions == nil {
		state.Sessions = newRecordCollectionWith(defaults.Sessions.All())
	}
	if state.SessionKeys == nil {
		state.SessionKeys = newRecordCollectionWith(defaults.SessionKeys.All())
	}
	if state.LibraryEntries == nil {
		state.LibraryEntries = newRecordCollectionWith(defaults.LibraryEntries.All())
	}
	if state.Snapshots == nil {
		state.Snapshots = newRecordCollectionWith(defaults.Snapshots.All())
	}
	if state.Chunks == nil {
		state.Chunks = newRecordCollectionWith(defaults.Chunks.All())
	}
	if state.SyncAttempts == nil {
		state.SyncAttempts = newRecordCollectionWith(defaults.SyncAttempts.All())
	}
	if state.ForkAttempts == nil {
		state.ForkAttempts = newRecordCollectionWith(defaults.ForkAttempts.All())
	}
	if state.WorkNodes == nil {
		state.WorkNodes = newRecordCollectionWith(defaults.WorkNodes.All())
	}
	if state.ProjectionJobs == nil {
		state.ProjectionJobs = newRecordCollectionWith(defaults.ProjectionJobs.All())
	}
	for key, session := range state.Sessions.All() {
		if session.RootServerSessionID == "" {
			session.RootServerSessionID = session.ServerSessionID
			state.Sessions.Set(key, session)
		}
	}
}

func validateStoredSchema(version int) error {
	if version >= 5 && version < 12 {
		return fmt.Errorf("unsupported unpublished V2 database schema %d; use a fresh metadata database and reimport Sessions", version)
	}
	return nil
}

func migrateDatabaseState(state *databaseState, version int) error {
	if err := validateStoredSchema(version); err != nil {
		return err
	}
	state.activityMigration = true
	defer func() { state.activityMigration = false }()
	if version < 16 {
		if err := migrateLibraryCheckpoints(state); err != nil {
			return err
		}
	}
	if version < 3 {
		if err := migrateNormalizedHistory(state); err != nil {
			return err
		}
	}
	if version < 6 {
		migrateHistoryBatches(state)
	}
	state.activityMigration = false
	if version < 17 {
		migrateProjectActivity(state)
	}
	return nil
}
