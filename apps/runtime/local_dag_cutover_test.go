package main

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
)

func TestReconnectUpgradesOnlyWorkflowAndKeepsStableAssociation(t *testing.T) {
	paths, target, remote := relayConfigurationFixture(t)
	old := fixtureBinding(currentDirectory(), remote.project.ProjectID, remote.account.AccountID, target)
	if err := saveBinding(paths, old); err != nil {
		t.Fatal(err)
	}
	if err := saveCredential(paths, target, remote.account, "fixture-token-with-enough-entropy-1234"); err != nil {
		t.Fatal(err)
	}
	saved, err := bindingAtRoot(paths, currentDirectory())
	if err != nil {
		t.Fatal(err)
	}
	old = *saved
	before, _, err := loadCredential(paths, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := managedLocalDAGRoot(); err == nil || !strings.Contains(err.Error(), "coconet connect") {
		t.Fatal("old binding silently activated old workflow", err)
	}
	var out, stderr bytes.Buffer
	if code := runConnect(context.Background(), nil, strings.NewReader(""), &out, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	got, err := bindingAtRoot(paths, currentDirectory())
	if err != nil || got == nil {
		t.Fatal(err)
	}
	want := old
	want.Workflow = localDAGWorkflow
	if !reflect.DeepEqual(*got, want) {
		t.Fatal("upgrade changed association", *got, want)
	}
	identity, err := loadLocalDAGIdentity(paths, false)
	if err != nil {
		t.Fatal(err)
	}
	config, err := loadLocalDAGService(localDAGBindingRoot(paths, *got))
	if err != nil || config.PauseReason != "" {
		t.Fatal("upgrade did not enable automatic inference", err)
	}
	out.Reset()
	stderr.Reset()
	if code := runConnect(context.Background(), nil, strings.NewReader(""), &out, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	next, _ := loadLocalDAGIdentity(paths, false)
	after, _, err := loadCredential(paths, target)
	if err != nil || identity != next || !reflect.DeepEqual(before, after) || remote.createTransactions != 0 {
		t.Fatal("reconnect replaced identity, credential or project", err)
	}
}

func TestNewBindingRequiresVerifiedLocalDAGCapability(t *testing.T) {
	paths, target, remote := relayConfigurationFixture(t)
	target.Capabilities = []string{contracts.DeploymentCapabilityRelay}
	if err := persistBinding(paths, currentDirectory(), target, remote.account.AccountID, remote.project.ProjectID, ""); err == nil {
		t.Fatal("accepted server without workflow capability")
	}
	bindings, err := loadBindingState(paths)
	if err != nil || len(bindings.Bindings) != 0 {
		t.Fatal("failed connect created binding", err)
	}
}
