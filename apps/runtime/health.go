package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
)

func runHealth(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 || len(args) == 1 && args[0] != "--json" {
		fmt.Fprintln(stderr, "usage: coconet health [--json]")
		return 2
	}
	if project, managed, err := resolveLocalMCPProject(); managed {
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		value, err := project.call(ctx, "status", localDAGClientOptions{})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if _, err := fmt.Fprintln(stdout, string(value)); err != nil {
			return 1
		}
		return 0
	}
	if err := json.NewEncoder(stdout).Encode(runtimeHealth(ctx, nil)); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func runtimeHealth(ctx context.Context, configured []adapters.Adapter) contracts.HealthReport {
	if configured == nil {
		configured = runtimeSessionAdapters()
	}
	components := []contracts.ComponentHealth{
		{Name: "runtime", Status: contracts.StatusOK, Version: version},
	}
	status := contracts.StatusOK
	for _, adapter := range configured {
		health := adapter.Health(ctx)
		if health.Status != contracts.StatusOK && health.FailureCategory == "" {
			health.FailureCategory = contracts.FailureAdapter
		}
		components = append(components, health)
		if health.Status != contracts.StatusOK {
			status = contracts.StatusDegraded
		}
	}
	paths, pathErr := resolveRuntimePaths()
	localState, localComponents := runtimeLocalHealth(paths, pathErr)
	components = append(components, localComponents...)
	for _, component := range localComponents {
		if component.Status != contracts.StatusOK {
			status = contracts.StatusDegraded
		}
	}
	return contracts.HealthReport{
		SchemaVersion: contracts.SchemaVersion,
		Status:        status,
		Component:     "runtime",
		Version:       version,
		Components:    components,
		RuntimeState:  &localState,
	}
}

func runtimeLocalHealth(paths runtimePaths, pathErr error) (contracts.RuntimeHealthState, []contracts.ComponentHealth) {
	state := contracts.RuntimeHealthState{}
	localComponent := contracts.ComponentHealth{Name: "local_state", Status: contracts.StatusOK}
	if pathErr != nil {
		localComponent.Status = contracts.StatusUnavailable
		localComponent.FailureCategory = contracts.FailureLocalState
		localComponent.Message = "Runtime state directory could not be resolved"
		return state, []contracts.ComponentHealth{localComponent}
	}
	targets, err := loadCredentialTargets(paths)
	if err != nil {
		localComponent.Status = contracts.StatusUnavailable
		localComponent.FailureCategory = contracts.FailureLocalState
		localComponent.Message = "Deployment credentials could not be read"
		return state, []contracts.ComponentHealth{localComponent}
	}
	bindings, err := loadBindingState(paths)
	if err != nil {
		localComponent.Status = contracts.StatusUnavailable
		localComponent.FailureCategory = contracts.FailureLocalState
		localComponent.Message = "Project bindings could not be read"
		return state, []contracts.ComponentHealth{localComponent}
	}
	state.DeploymentCount = len(targets)
	state.BindingCount = len(bindings.Bindings)
	targetByDeployment := make(map[string]deploymentTarget, len(targets))
	for _, target := range targets {
		targetByDeployment[target.DeploymentID] = target
	}
	for _, binding := range bindings.Bindings {
		if !runtimeBindingResolvable(binding, targetByDeployment) {
			state.InvalidBindingCount++
		}
	}
	if state.InvalidBindingCount > 0 {
		localComponent.Status = contracts.StatusDegraded
		localComponent.FailureCategory = contracts.FailureDeploymentIdentity
		localComponent.Message = "One or more Project bindings no longer resolve to their pinned Deployment"
	}
	for _, binding := range bindings.Bindings {
		if binding.Workflow != localDAGWorkflow {
			localComponent.Status = contracts.StatusDegraded
			localComponent.Message = "An existing folder connection needs upgrading: run coconet connect in that folder, then configure local inference."
		}
	}
	return state, []contracts.ComponentHealth{localComponent}
}

func runtimeBindingResolvable(binding contracts.LocalBinding, targets map[string]deploymentTarget) bool {
	target, exists := targets[binding.DeploymentID]
	return exists && binding.SchemaVersion == contracts.LocalBindingSchemaVersion && binding.ProjectID != "" && binding.AccountID != "" && binding.ServerPublicKeySHA256 == target.ServerPublicKeySHA256
}

func environmentOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
