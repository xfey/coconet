package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/xfey/coconet/packages/modelusage"
)

func usageLayoutFixture(name string, input int64) usageReport {
	counter := func(value int64) modelusage.Counter { return modelusage.Counter{Tokens: value, Reported: 1} }
	stats := modelusage.Stats{Attempts: 123, Input: counter(input), Output: counter(9876), CacheRead: counter(54321), CacheWrite: counter(100)}
	return usageReport{Periods: []usagePeriod{{Label: "Last 7 days", Totals: stats, Models: []modelusage.Group{{Provider: "codex", Key: name, Stats: stats}}}}}
}

func TestUsageLayoutFitsNarrowAndWideTerminalsWithoutLosingCounts(t *testing.T) {
	for _, columns := range []int{40, 60, 80, 120} {
		for _, name := range []string{"Alpha", strings.Repeat("long-model-name-", 6), "中文模型-带缓存-e\u0301"} {
			var out bytes.Buffer
			if err := printUsageReportWidth(&out, usageLayoutFixture(name, 12345678), columns); err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(out.String(), "\n") {
				if usageTextColumns(line) > columns {
					t.Fatalf("%d-column overflow: %q", columns, line)
				}
			}
			for _, value := range []string{"12,345,678", "9,876", "54,321", "100"} {
				if strings.Count(out.String(), value) != 2 {
					t.Fatalf("model/total lost %q: %s", value, out.String())
				}
			}
			if strings.ContainsAny(out.String(), "\t\x1b") {
				t.Fatal("terminal escapes or raw tabs in report")
			}
			if joined := strings.Join(wrapUsageLine(name, 12), ""); joined != name {
				t.Fatalf("model name was truncated: %q", joined)
			}
		}
	}
	var out bytes.Buffer
	if err := printUsageReportWidth(&out, usageLayoutFixture("Alpha", 9223372036854775807), 40); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "9,223,372,036,854,775,807") != 2 {
		t.Fatal("large counters truncated", out.String())
	}
}

func TestUsageNumericColumnsAlignRightIncludingWideModelNames(t *testing.T) {
	for _, name := range []string{"Alpha", "中文模型"} {
		var out bytes.Buffer
		if err := printUsageReportWidth(&out, usageLayoutFixture(name, 12345678), 120); err != nil {
			t.Fatal(err)
		}
		var header, row string
		for _, line := range strings.Split(out.String(), "\n") {
			if strings.HasPrefix(line, "Model") {
				header = line
			}
			if strings.HasPrefix(line, "codex /") {
				row = line
			}
		}
		values := regexp.MustCompile(`[0-9][0-9,]*`).FindAllStringIndex(row, -1)
		if len(values) != 5 {
			t.Fatal("missing numeric columns", out.String())
		}
		for i, label := range []string{"Attempts", "Input", "Output", "Cache read", "Cache write"} {
			end := strings.Index(header, label) + len(label)
			if usageTextColumns(row[:values[i][1]]) != usageTextColumns(header[:end]) {
				t.Fatalf("%s is not right aligned:\n%s", label, out.String())
			}
		}
	}
}
