package main

import (
	"encoding/json"

	"github.com/xfey/coconet/packages/contracts"
)

// Snapshot contents are part of the Snapshot aggregate. This view projects
// that aggregate for content readers; it owns no table or independent writes.
type snapshotContentView struct{ state *databaseState }

func (s *databaseState) snapshotContents() snapshotContentView { return snapshotContentView{s} }
func (v snapshotContentView) Lookup(key string) (normalizedVersionRecord, bool) {
	if old, ok := v.state.legacyIndexes[key]; ok {
		return old, true
	}

	snapshot, ok := v.state.Snapshots.Lookup(key)
	if !ok || snapshot.Conversation == nil {
		return normalizedVersionRecord{}, false
	}
	c := snapshot.Conversation
	return normalizedVersionRecord{ObservationSHA256: c.ObservationSHA256, Content: c.Content, Source: v.nativeCheckpoint(c.Summary), NativeLineage: c.NativeLineage, VisibleTextSHA256: c.VisibleTextSHA256, Summary: c.Summary}, true
}
func (v snapshotContentView) Get(key string) normalizedVersionRecord { r, _ := v.Lookup(key); return r }
func (v snapshotContentView) Has(key string) bool                    { _, ok := v.Lookup(key); return ok }
func (v snapshotContentView) Set(key string, c normalizedVersionRecord) {
	if c.Content == nil {
		if v.state.legacyIndexes == nil {
			v.state.legacyIndexes = map[string]normalizedVersionRecord{}
		}
		v.state.legacyIndexes[key] = c
		return
	}
	snapshot := v.state.Snapshots.Get(key)
	snapshot.Conversation = &contracts.SnapshotContent{ObservationSHA256: c.ObservationSHA256, Content: c.Content, NativeLineage: c.NativeLineage, VisibleTextSHA256: c.VisibleTextSHA256, Summary: c.Summary}
	if c.Source != nil {
		session := v.state.Sessions.Get(c.Summary.ServerSessionID)
		if session.SyncCheckpoint == nil || session.SyncCheckpoint.Version <= c.Summary.Version {
			session.SyncCheckpoint = &contracts.SessionSyncCheckpoint{Version: c.Summary.Version, SourceEpoch: c.Summary.SourceEpoch, Cursor: c.Summary.ToCursor, Native: *c.Source}
			v.state.Sessions.Set(session.ServerSessionID, session)
		}
	}
	v.state.Snapshots.Set(key, snapshot)
	delete(v.state.legacyIndexes, key)
}
func (v snapshotContentView) Delete(key string) {
	snapshot := v.state.Snapshots.Get(key)
	snapshot.Conversation = nil
	v.state.Snapshots.Set(key, snapshot)
	delete(v.state.legacyIndexes, key)
}
func (v snapshotContentView) All() map[string]normalizedVersionRecord {
	out := map[string]normalizedVersionRecord{}
	for key, s := range v.state.Snapshots.All() {
		if s.Conversation != nil {
			out[key] = v.Get(key)
		}
	}
	for key, c := range v.state.legacyIndexes {
		out[key] = c
	}
	return out
}
func (v snapshotContentView) Len() int { return len(v.All()) }

// Only migration fixtures/old schema decoding can contain legacy index bodies.
// A successful migration consumes them before writing the new record catalog.
func (s databaseState) MarshalJSON() ([]byte, error) {
	type plain databaseState
	if len(s.legacyIndexes) == 0 {
		return json.Marshal(plain(s))
	}
	return json.Marshal(struct {
		plain
		Indexes map[string]normalizedVersionRecord `json:"session_indexes"`
	}{plain(s), s.legacyIndexes})
}

func (v snapshotContentView) nativeCheckpoint(summary contracts.SessionIndexRecord) *contracts.ConversationManifest {
	c := v.state.Sessions.Get(summary.ServerSessionID).SyncCheckpoint
	if c == nil || c.Version != summary.Version {
		return nil
	}
	m := c.Native
	return &m
}
