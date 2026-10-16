package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/localdag"
)

func TestLocalRelayConfigurationPinsWriterAndRevisionSpace(t *testing.T) {
	root := t.TempDir()
	store, err := graphstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := &localdag.Engine{Store: store}
	tokenPath := filepath.Join(root, "credential")
	if err := os.WriteFile(tokenPath, []byte("test-credential"), 0600); err != nil {
		t.Fatal(err)
	}
	config := localDAGRelayConfig{SchemaVersion: 1, Endpoint: "http://127.0.0.1:1234", AccountID: "account", DeviceID: "device", TokenFile: tokenPath, Projects: []string{"project"}}
	path := filepath.Join(root, "relay.json")
	data, _ := json.Marshal(config)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := configureLocalDAGRelay(root, "", "project", engine); err != nil {
		t.Fatal(err)
	}
	if engine.Actor != "account.device" || engine.Publisher == nil {
		t.Fatal("Relay writer not configured")
	}
	projects, _ := store.Projects()
	if len(projects) != 1 {
		t.Fatal("empty subscribed project is not discoverable")
	}
	if err := store.BindMode("offline"); err == nil {
		t.Fatal("Relay store silently became offline")
	}
	if err := configureLocalDAGRelay(root, "", "unconfigured", engine); err == nil {
		t.Fatal("unconfigured project accepted")
	}
	if err := os.Chmod(tokenPath, 0644); err != nil {
		t.Fatal(err)
	}
	if err := configureLocalDAGRelay(root, "", "project", engine); err == nil {
		t.Fatal("publicly readable credentials accepted")
	}
}
