package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"time"
)

func runLocalProjectionCommand(ctx context.Context, project localMCPProject, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "status" && args[0] != "retry" {
		return 2
	}
	flags := flag.NewFlagSet("projection", flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "write device-local processing status")
	task := flags.String("task", "", "exact paused model or publication task")
	capture := flags.String("capture", "", "exact paused native capture event")
	history := flags.String("history-batch", "", "failed local history plan")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
		return 2
	}
	selections := 0
	for _, id := range []string{*task, *capture, *history} {
		if id != "" {
			selections++
		}
	}
	if args[0] == "status" && selections > 0 || args[0] == "retry" && selections != 1 {
		fmt.Fprintln(stderr, "Use projection status to inspect failures; retry exactly one --task ID, --capture ID or --history-batch ID.")
		return 2
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	action, options := "status", localDAGClientOptions{}
	if args[0] == "retry" {
		switch {
		case *task != "":
			action, options.Task = "retry", *task
		case *capture != "":
			action, options.Task = "retry-inbox", *capture
		case *history != "":
			action, options.History = "history-retry", &localHistoryRequest{PlanID: *history}
		}
	}
	value, err := project.call(ctx, action, options)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *asJSON {
		if _, err := fmt.Fprintln(stdout, string(value)); err != nil {
			return 1
		}
		return 0
	}
	if args[0] == "retry" {
		fmt.Fprintln(stdout, "The selected operation is queued on this device. Model and history budgets still apply. Check coconet projection status.")
		return 0
	}
	var status struct {
		Status    string               `json:"status"`
		Mode      string               `json:"sync_mode"`
		Attempts  int                  `json:"attempts"`
		Limit     int                  `json:"attempt_limit"`
		Paused    bool                 `json:"paused"`
		LastError string               `json:"last_error"`
		Project   localOperationStatus `json:"project"`
	}
	if err := json.Unmarshal(value, &status); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Runtime: %s · %s · inference paused: %t\nDaily calls: %d/%d\nAccepted DAG revision: %d · last confirmed checkpoint: %d\nPending publication: %d · capture events: %d (%d paused)\n", status.Status, status.Mode, status.Paused, status.Attempts, status.Limit, status.Project.Revision, status.Project.Published, status.Project.PendingCommits, status.Project.CapturePending, status.Project.CapturePaused)
	if status.LastError != "" {
		fmt.Fprintln(stdout, status.LastError)
	}
	phases := []string{}
	for phase := range status.Project.Tasks {
		phases = append(phases, phase)
	}
	sort.Strings(phases)
	for _, phase := range phases {
		fmt.Fprintf(stdout, "  %s: %d\n", phase, status.Project.Tasks[phase])
	}
	for _, issue := range status.Project.Issues {
		fmt.Fprintf(stdout, "\n%s · %s · %s\n  %s\n  %s\n", issue.Kind, issue.ID, issue.State, issue.Error, issue.Action)
		if issue.RunID != "" {
			fmt.Fprintln(stdout, "  Run: "+issue.RunID+" (inspect coconet diagnostics list)")
		}
	}
	if status.Project.IssueCount > len(status.Project.Issues) {
		fmt.Fprintf(stdout, "\nShowing %d of %d issues.\n", len(status.Project.Issues), status.Project.IssueCount)
	}
	if len(status.Project.RecentTransfers) > 0 {
		fmt.Fprintln(stdout, "\nRecent transport records (historical results; failures may have recovered):")
		for _, transfer := range status.Project.RecentTransfers {
			fmt.Fprintf(stdout, "  %d · %s · %s · %s\n", transfer.ID, transfer.Operation, transfer.Status, transfer.At)
			if transfer.Error != "" {
				fmt.Fprintln(stdout, "    "+transfer.Error)
			}
		}
	}
	fmt.Fprintln(stdout, "\n"+status.Project.Note)
	return 0
}
