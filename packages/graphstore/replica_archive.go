package graphstore

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/sessionartifact"
	"github.com/xfey/coconet/packages/workgraph"
	bolt "go.etcd.io/bbolt"
)

const maxReplicaManifest = 32 << 20
const maxReplicaObjects = 100000

type replicaArchive struct {
	Schema      int                     `json:"schema"`
	State       workgraph.State         `json:"state"`
	Head        workgraph.Commit        `json:"head"`
	StateSHA256 string                  `json:"state_sha256"`
	Objects     []contracts.RelayObject `json:"objects"`
}
type ReplicaArchiveResult struct {
	Project  string `json:"project"`
	Revision int64  `json:"revision"`
	Sources  int    `json:"sources"`
	Objects  int    `json:"objects"`
	Bytes    int64  `json:"bytes"`
	Offline  bool   `json:"offline"`
}

type archiveReader struct {
	ctx context.Context
	r   io.Reader
}

func (r archiveReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// replicaClosure verifies complete native/text content, not last-known progress.
func (s *Store) replicaClosure(ctx context.Context, state workgraph.State) (map[string]int64, error) {
	objects := map[string]int64{}
	add := func(digest string) error {
		if _, ok := objects[digest]; ok {
			return nil
		}
		if len(objects) >= maxReplicaObjects {
			return errors.New("replica archive object limit exceeded")
		}
		f, err := s.OpenObject(digest)
		if err != nil {
			return err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return err
		}
		objects[digest] = info.Size()
		return nil
	}
	for key := range workgraph.RetainedSources(state) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		snapshot := state.Snapshots[key]
		if err := s.WalkConversation(ctx, snapshot.Conversation, snapshot.Turns, func(conversation.Entry) error { return nil }); err != nil {
			return nil, fmt.Errorf("text replica incomplete: %w", err)
		}
		if err := add(snapshot.Conversation); err != nil {
			return nil, err
		}
		data, err := s.Object(snapshot.Conversation)
		if err != nil {
			return nil, err
		}
		var m conversation.Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, err
		}
		for _, block := range m.Blocks {
			if err := add(block.SHA256); err != nil {
				return nil, err
			}
		}
		if snapshot.Artifact == "" {
			continue
		}
		if err := s.ValidateArtifact(snapshot.Artifact); err != nil {
			return nil, fmt.Errorf("native replica incomplete: %w", err)
		}
		if err := add(snapshot.Artifact); err != nil {
			return nil, err
		}
		native, err := s.ArtifactManifest(snapshot.Artifact)
		if err != nil {
			return nil, err
		}
		for _, chunk := range native.Chunks {
			if err := add(sessionartifact.StoredHash(chunk)); err != nil {
				return nil, err
			}
		}
	}
	return objects, nil
}

// ExportReplica writes a private immutable archive of the accepted project and
// its currently retained closures. It contains no login credentials, inference
// archives, pending local proposals, derived exports or search index.
func (s *Store) ExportReplica(ctx context.Context, project, path string) (result ReplicaArchiveResult, failure error) {
	if !filepath.IsAbs(path) {
		return result, errors.New("archive path must be absolute")
	}
	release := s.PinContent()
	defer release()
	state, head, err := s.CheckpointState(project)
	if err != nil {
		return result, err
	}
	if state.Revision == 0 {
		return result, errors.New("project has no accepted graph to export")
	}
	if err := workgraph.ValidateState(state); err != nil {
		return result, err
	}
	objects, err := s.replicaClosure(ctx, state)
	if err != nil {
		return result, err
	}
	manifest := replicaArchive{Schema: 1, State: state, Head: head, StateSHA256: workgraph.Digest(state), Objects: []contracts.RelayObject{}}
	result = ReplicaArchiveResult{Project: project, Revision: state.Revision, Sources: len(workgraph.RetainedSources(state)), Objects: len(objects)}
	for hash, size := range objects {
		manifest.Objects = append(manifest.Objects, contracts.RelayObject{SHA256: hash, Size: size})
		result.Bytes += size
	}
	sort.Slice(manifest.Objects, func(i, j int) bool { return manifest.Objects[i].SHA256 < manifest.Objects[j].SHA256 })
	data, err := json.Marshal(manifest)
	if err != nil {
		return result, err
	}
	if len(data) > maxReplicaManifest {
		return result, errors.New("replica archive manifest limit exceeded")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".coconet-replica-*")
	if err != nil {
		return result, err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	tw := tar.NewWriter(file)
	if err := tw.WriteHeader(&tar.Header{Name: "replica.json", Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		return result, err
	}
	if _, err := tw.Write(data); err != nil {
		return result, err
	}
	for _, object := range manifest.Objects {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := tw.WriteHeader(&tar.Header{Name: "objects/" + object.SHA256, Mode: 0600, Size: object.Size, Typeflag: tar.TypeReg}); err != nil {
			return result, err
		}
		f, err := s.OpenObject(object.SHA256)
		if err != nil {
			return result, err
		}
		_, copyErr := io.Copy(tw, archiveReader{ctx, f})
		err = errors.Join(copyErr, f.Close())
		if err != nil {
			return result, err
		}
	}
	if err := tw.Close(); err != nil {
		return result, err
	}
	if err := file.Sync(); err != nil {
		return result, err
	}
	if err := file.Close(); err != nil {
		return result, err
	}
	// Link, rather than rename, refuses to overwrite any existing user file.
	if err := os.Link(file.Name(), path); err != nil {
		return result, err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return result, err
	}
	defer directory.Close()
	return result, directory.Sync()
}

// RestoreReplica never replaces an existing graph. Connected projects must
// first synchronize their graph; only matching, still-retained sources receive
// readiness records. A new unbound root becomes an offline recovery workspace.
func (s *Store) RestoreReplica(ctx context.Context, project, path string) (result ReplicaArchiveResult, failure error) {
	if !filepath.IsAbs(path) {
		return result, errors.New("archive path must be absolute")
	}
	release := s.PinContent()
	defer release()
	info, err := os.Lstat(path)
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() {
		return result, errors.New("replica archive must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer file.Close()
	tr := tar.NewReader(archiveReader{ctx, file})
	header, err := tr.Next()
	if err != nil {
		return result, err
	}
	if header.Name != "replica.json" || header.Typeflag != tar.TypeReg || header.Size < 1 || header.Size > maxReplicaManifest {
		return result, errors.New("invalid replica manifest envelope")
	}
	var manifest replicaArchive
	decoder := json.NewDecoder(tr)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return result, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return result, errors.New("trailing replica manifest")
	}
	state := manifest.State
	if manifest.Schema != 1 || state.ProjectID != project || state.Revision < 1 || workgraph.Digest(state) != manifest.StateSHA256 || manifest.Head.ProjectID != project || manifest.Head.Base+1 != state.Revision || manifest.Head.ID == "" || len(manifest.Objects) > maxReplicaObjects {
		return result, errors.New("replica archive identity differs")
	}
	if err := workgraph.ValidateState(state); err != nil {
		return result, err
	}
	// Reject identity conflicts before copying any data; repeat in the install
	// transaction since the normal daemon may receive newer graph commits.
	if err := s.db.View(func(tx *bolt.Tx) error { return validateReplicaTarget(tx, state) }); err != nil {
		return result, err
	}
	expected := map[string]int64{}
	var total int64
	for _, object := range manifest.Objects {
		if !validArchiveHash(object.SHA256) || object.Size < 0 || object.Size > MaxObjectBytes {
			return result, errors.New("invalid replica object")
		}
		if _, exists := expected[object.SHA256]; exists {
			return result, errors.New("duplicate replica object")
		}
		total += object.Size
		if total > s.objectLimit.Load() {
			return result, ErrLocalObjectQuota
		}
		expected[object.SHA256] = object.Size
	}
	seen := map[string]bool{}
	for {
		header, err = tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return result, err
		}
		if header.Typeflag != tar.TypeReg || len(header.Name) != len("objects/")+64 || header.Name[:len("objects/")] != "objects/" {
			return result, errors.New("invalid archive entry")
		}
		hash := header.Name[len("objects/"):]
		size, ok := expected[hash]
		if !ok || seen[hash] || size != header.Size {
			return result, errors.New("unexpected replica object")
		}
		if err := s.Receive(tr, hash); err != nil {
			return result, err
		}
		seen[hash] = true
	}
	if len(seen) != len(expected) {
		return result, errors.New("incomplete replica archive")
	}
	closure, err := s.replicaClosure(ctx, state)
	if err != nil {
		return result, err
	}
	if len(closure) != len(expected) {
		return result, errors.New("archive includes unrelated objects")
	}
	for hash, size := range closure {
		if archivedSize, ok := expected[hash]; !ok || archivedSize != size {
			return result, errors.New("archive closure differs")
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result = ReplicaArchiveResult{Project: project, Objects: len(seen), Bytes: total}
	err = s.db.Update(func(tx *bolt.Tx) error {
		if err := validateReplicaTarget(tx, state); err != nil {
			return err
		}
		current, err := readState(tx, project)
		if err != nil {
			return err
		}
		if current.Revision == 0 {
			data, _ := json.Marshal(state)
			if err := tx.Bucket([]byte("replica-baselines")).Put([]byte(project), data); err != nil {
				return err
			}
			if err := tx.Bucket([]byte("states")).Put([]byte(project), data); err != nil {
				return err
			}
			data, _ = json.Marshal(manifest.Head)
			if err := tx.Bucket([]byte("heads")).Put([]byte(project), data); err != nil {
				return err
			}
			if err := tx.Bucket([]byte("settings")).Put([]byte("mode"), []byte("offline")); err != nil {
				return err
			}
			current = state
			result.Offline = true
		}
		result.Revision = current.Revision
		retainedSaved := workgraph.RetainedSources(state)
		for sourceKey := range workgraph.RetainedSources(current) {
			snapshot := current.Snapshots[sourceKey]
			saved, ok := state.Snapshots[sourceKey]
			if !ok || workgraph.Digest(saved) != workgraph.Digest(snapshot) || !retainedSaved[sourceKey] {
				continue
			}
			data, _ := json.Marshal(ReplicaState{Source: snapshot.Source, Digest: snapshot.Digest, TextReady: true, NativeReady: true, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
			if err := tx.Bucket([]byte("replicas")).Put(key(project, sourceKey), data); err != nil {
				return err
			}
			result.Sources++
		}
		return nil
	})
	return result, err
}
func validArchiveHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	for _, c := range hash {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func validateReplicaTarget(tx *bolt.Tx, saved workgraph.State) error {
	current, err := readState(tx, saved.ProjectID)
	if err != nil {
		return err
	}
	if current.Revision == 0 {
		mode := string(tx.Bucket([]byte("settings")).Get([]byte("mode")))
		if mode != "" && mode != "offline" {
			return errors.New("sync this connected project's graph before restoring its content")
		}
		return bootstrapReady(tx, saved.ProjectID)
	}
	for key, snapshot := range current.Snapshots {
		if old, ok := saved.Snapshots[key]; ok && workgraph.Digest(old) != workgraph.Digest(snapshot) {
			return errors.New("archive source differs from current project")
		}
	}
	return nil
}

// Keep backup baselines distinct from remotely verified checkpoint receipts.
func replicaReplayBase(tx *bolt.Tx, project string) (*workgraph.State, error) {
	data := tx.Bucket([]byte("replica-baselines")).Get([]byte(project))
	if data == nil {
		return nil, nil
	}
	var state workgraph.State
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&state); err != nil {
		return nil, err
	}
	return &state, workgraph.ValidateState(state)
}
