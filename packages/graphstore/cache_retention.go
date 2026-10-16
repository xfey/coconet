package graphstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/conversation"
	"github.com/xfey/coconet/packages/sessionartifact"
	"github.com/xfey/coconet/packages/workgraph"
	bolt "go.etcd.io/bbolt"
)

const localObjectProtection = 7 * 24 * time.Hour

type CacheRoots struct {
	Objects   []string
	Artifacts []string
}
type CacheCollection struct {
	Removed int   `json:"removed"`
	Bytes   int64 `json:"bytes"`
	Busy    bool  `json:"busy"`
}

// PruneCache deletes at most 512 expired derived files and 512 old unreferenced
// content objects. Each deletion is independent; errors can follow partial work. Reference
// discovery and deletion exclude complete content operations and graph writes.
// Supplied roots cover durable capture inbox entries outside the graph database.
func (s *Store) PruneCache(ctx context.Context, now time.Time, actor string, extra CacheRoots) (CacheCollection, error) {
	var result CacheCollection
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.contentPins > 0 {
		result.Busy = true
		return result, nil
	}
	derived, err := s.pruneMaterialized(ctx, now)
	if err != nil {
		return derived, err
	}
	result = derived
	live := map[string]bool{}
	for _, digest := range extra.Objects {
		live[digest] = true
	}
	type root struct {
		digest   string
		native   bool
		required bool
	}
	roots := []root{}
	for _, digest := range extra.Artifacts {
		roots = append(roots, root{digest, true, true})
	}
	add := func(snapshot workgraph.Snapshot, required bool) {
		roots = append(roots, root{snapshot.Conversation, false, required})
		if snapshot.Artifact != "" {
			roots = append(roots, root{snapshot.Artifact, true, required})
		}
	}
	if err := s.db.View(func(tx *bolt.Tx) error {
		if err := tx.Bucket([]byte("states")).ForEach(func(_, data []byte) error {
			var state workgraph.State
			if err := json.Unmarshal(data, &state); err != nil {
				return err
			}
			for _, snapshot := range state.Snapshots {
				add(snapshot, actor == "" || snapshot.Owner == actor)
			}
			return nil
		}); err != nil {
			return err
		}
		return tx.Bucket([]byte("pending")).ForEach(func(_, data []byte) error {
			var commit workgraph.Commit
			if err := json.Unmarshal(data, &commit); err != nil {
				return err
			}
			if commit.Snapshot != nil {
				add(*commit.Snapshot, true)
			}
			return nil
		})
	}); err != nil {
		return result, err
	}
	visited := map[string]bool{}
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		live[root.digest] = true
		if visited[root.digest] {
			continue
		}
		data, err := s.objects.Object(root.digest)
		if errors.Is(err, os.ErrNotExist) && !root.required {
			continue
		}
		if err != nil {
			return result, err
		}
		visited[root.digest] = true
		if root.native {
			var manifest contracts.ArtifactManifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				return result, err
			}
			if err := sessionartifact.ValidateManifest(manifest); err != nil {
				return result, err
			}
			for _, chunk := range manifest.Chunks {
				live[sessionartifact.StoredHash(chunk)] = true
			}
		} else {
			var manifest conversation.Manifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				return result, err
			}
			if err := conversation.Validate(manifest); err != nil {
				return result, err
			}
			for _, block := range manifest.Blocks {
				live[block.SHA256] = true
			}
		}
	}
	objects, err := s.objects.Inventory(ctx)
	if err != nil {
		return result, err
	}
	var bytes int64
	for _, object := range objects {
		bytes += object.Size
	}
	s.objectBytes.Store(bytes)
	for _, object := range objects {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if live[object.SHA256] || now.Sub(object.Modified) < localObjectProtection {
			continue
		}
		if err := s.objects.Remove(object.SHA256); err != nil {
			return result, err
		}
		result.Removed++
		result.Bytes += object.Size
		s.objectBytes.Add(-object.Size)
		if result.Removed-derived.Removed >= 512 {
			break
		}
	}
	return result, nil
}
