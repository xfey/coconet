package contracts

import (
	"bytes"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestCheckpointProtocolBoundsAndHostedEnvelope(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	data, err := contractFiles.ReadFile("schemas/v1/relay.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	url := "https://schemas.coddis.dev/v1/relay.schema.json"
	if err := compiler.AddResource(url, document); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := compiler.Compile(url + "#/$defs/checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"schema_version": 1, "device_id": "device", "revision": 1, "commit_id": "commit", "codec": "coconet-state-json-v1", "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "payload": "e30="}
	if err := checkpoint.Validate(input); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"revision", "commit_id", "sha256"} {
		value := input[field]
		delete(input, field)
		if err := checkpoint.Validate(input); err == nil {
			t.Fatal("missing required checkpoint field", field)
		}
		input[field] = value
	}
	input["revision"] = 0
	if err := checkpoint.Validate(input); err == nil {
		t.Fatal("revision zero accepted")
	}
	input["revision"] = 1
	input["extra"] = true
	if err := checkpoint.Validate(input); err == nil {
		t.Fatal("unknown field accepted")
	}
	view, err := compiler.Compile(url + "#/$defs/published_graph")
	if err != nil {
		t.Fatal(err)
	}
	if err := view.Validate(map[string]any{"schema_version": 1, "event_revision": 1, "authors": map[string]any{}, "relay": map[string]any{"schema_version": 1, "head": 0, "generation": 0, "checkpoint": nil}}); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionProtocolRejectsAmbiguousPlans(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	data, err := contractFiles.ReadFile("schemas/v1/relay.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	url := "https://schemas.coddis.dev/v1/relay.schema.json"
	if err := compiler.AddResource(url, document); err != nil {
		t.Fatal(err)
	}
	lease, err := compiler.Compile(url + "#/$defs/read_lease_request")
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Validate(map[string]any{"schema_version": 1, "device_id": "device", "revision": 0}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []map[string]any{{"schema_version": 1, "device_id": "device", "revision": -1}, {"schema_version": 1, "device_id": "../device", "revision": 0}, {"schema_version": 1, "device_id": "device", "revision": 0, "lease_id": ""}} {
		if err := lease.Validate(bad); err == nil {
			t.Fatal("invalid lease accepted", bad)
		}
	}
	plan, err := compiler.Compile(url + "#/$defs/collection_plan")
	if err != nil {
		t.Fatal(err)
	}
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	input := map[string]any{"schema_version": 1, "id": "plan", "device_id": "device", "revision": 1, "checkpoint_sha256": hash, "objects": []any{hash}}
	if err := plan.Validate(input); err != nil {
		t.Fatal(err)
	}
	input["objects"] = []any{hash, hash}
	if err := plan.Validate(input); err == nil {
		t.Fatal("duplicate deletion candidate accepted")
	}
	input["objects"] = []any{}
	if err := plan.Validate(input); err == nil {
		t.Fatal("empty collection accepted")
	}
	input["objects"] = []any{hash}
	input["revision"] = 0
	if err := plan.Validate(input); err == nil {
		t.Fatal("unanchored collection accepted")
	}
}
