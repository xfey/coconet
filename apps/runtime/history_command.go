package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

func runHistoryCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || !slices.Contains([]string{"preview", "list", "status", "start", "pause", "resume", "retry", "forget"}, args[0]) {
		fmt.Fprintln(stderr, "usage: coconet history preview --agent codex|claude|cursor | list | status --plan ID | start --plan ID --sessions ID,ID [--batch-size 5] | pause|resume|retry|forget --plan ID")
		return 2
	}
	flags := flag.NewFlagSet("history "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	request := localHistoryRequest{}
	flags.StringVar(&request.PlanID, "plan", "", "exact preview/plan ID")
	agent := flags.String("agent", "", "native history provider: codex or claude")
	sessions := flags.String("sessions", "", "comma-separated native IDs explicitly chosen from this preview")
	flags.IntVar(&request.BatchSize, "batch-size", 5, "selected sessions per batch, 1–20; resume starts the next batch")
	asJSON := flags.Bool("json", false, "write structured result")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
		return 2
	}
	project, handled, err := resolveLocalMCPProject()
	if err != nil || !handled {
		fmt.Fprintln(stderr, "Connect this folder to a Relay project before importing history.")
		return 1
	}
	request.Agent = contracts.AgentType(*agent)
	if *sessions != "" {
		request.Sessions = strings.Split(*sessions, ",")
	}
	if args[0] == "preview" {
		if !contracts.ValidAgent(request.Agent) {
			fmt.Fprintln(stderr, "Choose --agent codex or claude.")
			return 2
		}
		config, err := loadLocalDAGService(project.root)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		cwd, err := canonicalPath(currentDirectory())
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		for _, workspace := range config.Workspaces {
			if workspace.ProjectID == project.project && pathContains(workspace.Path, cwd) && len(workspace.Path) > len(request.Root) {
				request.Root = workspace.Path
			}
		}
		request.ConfigRoot = libraryConfigRoot(request.Agent)
	} else if args[0] != "list" && request.PlanID == "" {
		fmt.Fprintln(stderr, "Select --plan from history preview or list.")
		return 2
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	value, err := project.call(ctx, "history-"+args[0], localDAGClientOptions{History: &request})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *asJSON {
		_, err = fmt.Fprintln(stdout, string(value))
		if err != nil {
			return 1
		}
		return 0
	}
	if args[0] == "forget" {
		fmt.Fprintln(stdout, "History plan metadata removed. Published work and native history are unchanged.")
		return 0
	}
	var plans []localHistoryPlan
	if args[0] == "list" {
		err = json.Unmarshal(value, &plans)
	} else {
		var plan localHistoryPlan
		err = json.Unmarshal(value, &plan)
		plans = append(plans, plan)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	for _, plan := range plans {
		if args[0] == "list" {
			fmt.Fprintf(stdout, "%s · %s · %s · calls %d\n%s\n", plan.ID, plan.Status, plan.Agent, plan.UsedCalls, plan.ScopeNote)
			continue
		}
		fmt.Fprintf(stdout, "%s · %s\nAgent: %s · model: Agent default\nCalls: %d · sessions: %d · batch through %d\n", plan.ID, plan.Status, plan.Agent, plan.UsedCalls, len(plan.Items), plan.BatchThrough)
		if plan.Status == "preview" {
			fmt.Fprintln(stdout, plan.ScopeNote)
			for _, session := range plan.Candidates {
				fmt.Fprintf(stdout, "  %s · %s · %s\n", session.NativeSessionID, session.ActivityState, displayTimestamp(session.UpdatedAt))
			}
			fmt.Fprintf(stdout, "Start with selected IDs:\n  coconet history start --plan %s --sessions ID,ID --batch-size 5\n", plan.ID)
		} else {
			for _, item := range plan.Items {
				fmt.Fprintf(stdout, "  %s · %s\n", item.Session.NativeSessionID, item.Status)
				if item.Error != "" {
					fmt.Fprintln(stdout, "    "+item.Error)
				}
			}
			fmt.Fprintln(stdout, "Pause prevents new calls; an in-flight call and accepted publication may finish. Use status for progress, resume for the next batch, retry for failed fixed inputs.")
		}
	}
	return 0
}
