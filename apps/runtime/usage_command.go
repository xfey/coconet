package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/modelusage"
	"github.com/xfey/coconet/packages/workgraph"
)

type usagePeriod struct {
	Label  string
	Totals modelusage.Stats
	Models []modelusage.Group
}

type usageReport struct {
	Periods  []usagePeriod
	Warnings []string
}

func runUsage(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "Usage: coconet usage (no options required)")
		return 2
	}
	report, err := collectUsage(ctx, time.Now().UTC())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := printUsageReport(stdout, report); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	for _, warning := range report.Warnings {
		fmt.Fprintln(stderr, terminalUsageText(warning))
	}
	if len(report.Warnings) > 0 {
		return 1
	}
	return 0
}

func collectUsage(ctx context.Context, now time.Time) (usageReport, error) {
	paths, err := resolveRuntimePaths()
	if err != nil {
		return usageReport{}, err
	}
	roots, err := installationScopeRoots(paths)
	if err != nil {
		return usageReport{}, fmt.Errorf("could not enumerate local Runtime stores: %w", err)
	}
	report := usageReport{}
	attempts := []modelusage.Attempt{}
	for _, root := range roots {
		if ctx.Err() != nil {
			return usageReport{}, ctx.Err()
		}
		_, statErr := os.Lstat(filepath.Join(root, "graph.db"))
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		id := workgraph.Digest(root)
		var rows []modelusage.Attempt
		err := statErr
		if err == nil {
			// Read once so all three periods use the same local snapshot.
			rows, err = readScopeUsage(ctx, root, time.Time{})
		}
		if err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("Store %s: %s", id[:8], err))
			continue
		}
		for _, row := range rows {
			row.Scope = id
			attempts = append(attempts, row)
		}
	}
	for _, period := range []struct {
		label string
		days  int
	}{{"Last 7 days", 7}, {"Last 30 days", 30}, {"All time", 0}} {
		selected := attempts
		if period.days > 0 {
			selected = nil
			since := now.Add(-time.Duration(period.days) * 24 * time.Hour)
			for _, row := range attempts {
				at, err := time.Parse(time.RFC3339Nano, row.StartedAt)
				if err == nil && !at.Before(since) {
					selected = append(selected, row)
				}
			}
		}
		total, models, err := modelusage.Summarize(selected, "model")
		if err != nil {
			return usageReport{}, err
		}
		report.Periods = append(report.Periods, usagePeriod{Label: period.label, Totals: total, Models: models})
	}
	return report, nil
}

func readScopeUsage(ctx context.Context, root string, since time.Time) ([]modelusage.Attempt, error) {
	query := ""
	if !since.IsZero() {
		query = since.Format(time.RFC3339Nano)
	}
	// This is a metadata-only report from the authenticated local worker.
	// All-history reports must not inherit the content-tool response limit.
	var output bytes.Buffer
	var diagnostic bytes.Buffer
	handled, code := localDAGClient(ctx, root, "usage", localDAGClientOptions{Query: query}, &output, &diagnostic)
	if handled {
		if code != 0 {
			return nil, fmt.Errorf("local usage is unavailable; an older running Runtime may need `coconet update`. %s", strings.TrimSpace(diagnostic.String()))
		}
		var rows []modelusage.Attempt
		if err := json.Unmarshal(output.Bytes(), &rows); err != nil {
			return nil, fmt.Errorf("invalid local usage response: %w", err)
		}
		return rows, nil
	}
	return graphstore.ReadModelUsage(root, since)
}

func terminalUsageText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
}

func tokenCount(value modelusage.Counter) string {
	if value.Reported == 0 {
		return "—"
	}
	text := strconv.FormatInt(value.Tokens, 10)
	for i := len(text) - 3; i > 0; i -= 3 {
		text = text[:i] + "," + text[i:]
	}
	return text
}

func printUsageReport(out io.Writer, report usageReport) error {
	return printUsageReportWidth(out, report, usageTerminalColumns(out))
}

func printUsageReportWidth(out io.Writer, report usageReport, columns int) error {
	var text strings.Builder
	text.WriteString("Coconet usage · background processing on this device · all projects\n")
	overlappingModels := false
	for _, period := range report.Periods {
		fmt.Fprintf(&text, "\n%s\n", period.Label)
		if period.Totals.Attempts == 0 {
			text.WriteString("No recorded usage.\n")
			continue
		}
		rows := [][]string{{"Model", "Attempts", "Input", "Output", "Cache read", "Cache write"}}
		row := func(label string, s modelusage.Stats) {
			rows = append(rows, []string{terminalUsageText(label), strconv.Itoa(s.Attempts), tokenCount(s.Input), tokenCount(s.Output), tokenCount(s.CacheRead), tokenCount(s.CacheWrite)})
		}
		modelAttempts := 0
		for _, model := range period.Models {
			row(model.Provider+" / "+model.Key, model.Stats)
			modelAttempts += model.Stats.Attempts
		}
		row("Total", period.Totals)
		writeUsageTable(&text, rows, columns)
		overlappingModels = overlappingModels || modelAttempts > period.Totals.Attempts
	}
	text.WriteString("\nReported tokens only; input includes cache tokens.\n— = unreported.\n")
	if overlappingModels {
		text.WriteString("One processing attempt may use multiple models.\n")
	}
	if len(report.Warnings) > 0 {
		text.WriteString("Some local stores could not be read; totals are incomplete.\n")
	}
	// Wrap prose too, so headings and notes do not force a narrow terminal to
	// wrap independently of the table. Redirected output stays plain text.
	for _, line := range strings.Split(strings.TrimSuffix(text.String(), "\n"), "\n") {
		for _, part := range wrapUsageLine(line, columns) {
			if _, err := fmt.Fprintln(out, part); err != nil {
				return err
			}
		}
	}
	return nil
}
