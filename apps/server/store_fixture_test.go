package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
	bolt "go.etcd.io/bbolt"
)

// Historical migration fixtures explicitly recreate the old on-disk layout;
// downgrading only the marker no longer creates a valid legacy database.
func snapshotStateBytes(t testing.TB, tx *bolt.Tx) []byte {
	t.Helper()
	if data := tx.Bucket(stateBucket).Get(stateKey); data != nil {
		return bytes.Clone(data)
	}
	context := &recordTransaction{active: true}
	defer func() { context.active = false }()
	state, _, err := bindRecordState(tx, context)
	if err != nil {
		t.Fatal(err)
	}
	state.recentFailureRecords()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func setFixtureSchema(t testing.TB, tx *bolt.Tx, version []byte) error {
	t.Helper()
	if tx.Bucket(recordsBucket) != nil && tx.Bucket(stateBucket).Get(stateKey) == nil {
		if err := tx.Bucket(stateBucket).Put(stateKey, snapshotStateBytes(t, tx)); err != nil {
			return err
		}
	}
	return tx.Bucket(metaBucket).Put(schemaKey, version)
}

func uniqueSessionKey(projectID string, agent contracts.AgentType, accountID, nativeSessionID string) string {
	// PostgreSQL JSONB rejects object keys containing the NUL separator used by
	// the local bbolt queue. Hash a canonical tuple so the key remains
	// unambiguous and can be stored by every metadata backend.
	content, _ := json.Marshal([]string{projectID, string(agent), accountID, nativeSessionID})
	digest := sha256.Sum256(content)
	return "session:" + hex.EncodeToString(digest[:])
}
