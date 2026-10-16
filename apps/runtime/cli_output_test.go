package main

import (
	"bytes"
	"testing"
)

func TestCLIFieldsUseAlignedBorderlessLayout(t *testing.T) {
	var output bytes.Buffer
	writeCLIFields(&output,
		cliField{Label: "Project", Value: "example-project"},
		cliField{Label: "Sync", Value: "No pending uploads"},
	)
	if actual, expected := output.String(), "Project  example-project\nSync     No pending uploads\n"; actual != expected {
		t.Fatalf("unexpected CLI fields\nactual:   %q\nexpected: %q", actual, expected)
	}
}

func TestCLIStatusLabelsHostedAndSyncState(t *testing.T) {
	if !isHostedServer("https://api.coconet.space/") || isHostedServer("https://coconet.example.com") {
		t.Fatal("Hosted endpoint classification is incorrect")
	}

}
