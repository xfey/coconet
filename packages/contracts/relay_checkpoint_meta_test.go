package contracts

import (
	"encoding/json"
	"testing"
)

func TestCheckpointEnvelopeCapacity(t *testing.T) {
	data, err := json.Marshal(RelayCheckpoint{SchemaVersion: 1, DeviceID: "device", Revision: 1, CommitID: "commit", Codec: "coconet-state-json-v1", SHA256: string(make([]byte, 64)), Payload: make([]byte, MaxRelayCheckpointBytes)})
	if err != nil {
		t.Fatal(err)
	}
	if len(data) <= 20<<20 || len(data) > MaxRelayCheckpointEnvelopeBytes {
		t.Fatalf("encoded maximum %d exceeds aligned proxy limit %d", len(data), MaxRelayCheckpointEnvelopeBytes)
	}
}
