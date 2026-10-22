package modelusage

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func requireTokens(t *testing.T, got *int64, want int64) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("tokens=%v, want %d", got, want)
	}
}

func envelopeFixture(t *testing.T, visible string) string {
	t.Helper()
	data, err := json.Marshal(map[string]string{"visible": visible, "text": "private final response"})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestClaudeIncludesAuxiliaryModelsWithoutAddingTopLevelAgain(t *testing.T) {
	visible := `startup notice
{"type":"result","total_cost_usd":0.04,"usage":{"input_tokens":4,"output_tokens":20},"modelUsage":{"main":{"inputTokens":4,"cacheReadInputTokens":200,"cacheCreationInputTokens":100,"outputTokens":20,"thinkingTokens":5,"costUSD":0.03},"aux":{"inputTokens":40,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"outputTokens":3,"costUSD":0.01}}}`
	u := Normalize("claude", "main, aux", json.RawMessage(`{"input_tokens":4,"cache_read_input_tokens":200,"cache_creation_input_tokens":100,"output_tokens":20}`), envelopeFixture(t, visible))
	if len(u.Models) != 2 || u.Models[0].Name != "aux" || u.Models[1].Name != "main" {
		t.Fatal(u)
	}
	requireTokens(t, u.Models[0].Tokens.Input, 40)
	requireTokens(t, u.Models[1].Tokens.Input, 304)
	requireTokens(t, u.Models[1].Tokens.Output, 20)
	requireTokens(t, u.Models[1].Tokens.Reasoning, 5)
	if u.EstimatedUSD == nil || *u.EstimatedUSD != .04 {
		t.Fatal(u)
	}
	encoded, _ := json.Marshal(u)
	if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "startup") {
		t.Fatal("receipt content leaked into counters")
	}
}

func TestCodexUsesCumulativeTotalOnceAndPreservesUnknownWriteCount(t *testing.T) {
	u := Normalize("codex", "model", json.RawMessage(`{"total":{"inputTokens":1000,"cachedInputTokens":800,"outputTokens":150,"reasoningOutputTokens":100},"last":{"inputTokens":100,"cachedInputTokens":0,"outputTokens":10}}`), "")
	m := u.Models[0]
	requireTokens(t, m.Tokens.Input, 1000)
	requireTokens(t, m.Tokens.CacheRead, 800)
	requireTokens(t, m.Tokens.Output, 150)
	requireTokens(t, m.Tokens.Reasoning, 100)
	if m.Tokens.CacheWrite != nil || m.Tokens.Uncached != nil || m.EstimatedUSD != nil {
		t.Fatal("invented unreported counters", m)
	}
}

func TestUsageVariantsAndMissingZeroDistinction(t *testing.T) {
	for _, tc := range []struct {
		name, provider, usage string
		input, output         int64
		cache                 *int64
	}{
		{"anthropic", "claude", `{"input_tokens":10,"cache_read_input_tokens":30,"cache_creation_input_tokens":20,"output_tokens":5}`, 60, 5, intPointer(30)},
		{"cursor-anthropic", "cursor", `{"input_tokens":10,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":5}`, 10, 5, intPointer(0)},
		{"cursor-completions", "cursor", `{"prompt_tokens":80,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":50},"completion_tokens_details":{"reasoning_tokens":2}}`, 80, 5, intPointer(50)},
		{"cursor-responses", "cursor", `{"input_tokens":80,"output_tokens":5,"input_tokens_details":{"cached_tokens":50,"cache_write_tokens":10}}`, 80, 5, intPointer(50)},
		{"last-only", "codex", `{"last":{"inputTokens":40,"outputTokens":2,"cachedInputTokens":0}}`, 40, 2, intPointer(0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := Normalize(tc.provider, "", json.RawMessage(tc.usage), "")
			requireTokens(t, u.Models[0].Tokens.Input, tc.input)
			requireTokens(t, u.Models[0].Tokens.Output, tc.output)
			requireTokens(t, u.Models[0].Tokens.CacheRead, *tc.cache)
			if u.Models[0].Name != "unknown" {
				t.Fatal(u)
			}
		})
	}
	unknown := Normalize("cursor", "", nil, "").Models[0].Tokens
	if unknown.Input != nil || unknown.Output != nil || unknown.CacheRead != nil {
		t.Fatal(unknown)
	}
	partial := Normalize("claude", "m", json.RawMessage(`{"input_tokens":5,"output_tokens":0}`), "").Models[0].Tokens
	if partial.Input != nil || partial.CacheRead != nil {
		t.Fatal("missing cache treated as zero", partial)
	}
	requireTokens(t, partial.Uncached, 5)
	requireTokens(t, partial.Output, 0)
	archived := Normalize("claude", "m", nil, envelopeFixture(t, `{"type":"result","usage":{"input_tokens":5,"cache_read_input_tokens":20,"cache_creation_input_tokens":10,"output_tokens":2}}`))
	requireTokens(t, archived.Models[0].Tokens.Input, 35)
	requireTokens(t, archived.Models[0].Tokens.Output, 2)
}

func TestMalformedUsageNeverBecomesFreeOrNegativeSpend(t *testing.T) {
	u := Normalize("codex", "m", json.RawMessage(`{"total":{"inputTokens":20,"cachedInputTokens":40,"outputTokens":-1,"reasoningOutputTokens":1.5}}`), "")
	m := u.Models[0]
	if m.Tokens.Output != nil || m.Tokens.Reasoning != nil || m.Tokens.CacheRead != nil {
		t.Fatal(m)
	}
	u = Normalize("claude", "m", json.RawMessage(`{"input_tokens":9223372036854775807,"cache_read_input_tokens":1,"cache_creation_input_tokens":0,"output_tokens":"20"}`), envelopeFixture(t, `{"type":"result","total_cost_usd":-1}`))
	if u.Models[0].Tokens.Input != nil || u.Models[0].Tokens.Output != nil || u.EstimatedUSD != nil {
		t.Fatal(u)
	}
	for _, raw := range []string{"{", "null", "[]", `{"unknown":100}`} {
		u := Normalize("cursor", "m", json.RawMessage(raw), envelopeFixture(t, "secret text without a valid result"))
		if u.Models[0].Tokens.Input != nil || u.Models[0].Tokens.Output != nil {
			t.Fatal(u)
		}
	}
}

func intPointer(v int64) *int64 { return &v }

func TestSummaryWeightsCacheAndCountsPhysicalRetriesAcrossScopes(t *testing.T) {
	a := Attempt{ID: "same", TaskID: "task", Scope: "one", ProjectID: "p", Provider: "codex", Kind: "stage", Status: "failed", Usage: Normalize("codex", "m", json.RawMessage(`{"total":{"inputTokens":100,"cachedInputTokens":80,"outputTokens":10}}`), "")}
	b := a
	b.ID, b.Retry, b.Status = "retry", true, "completed"
	b.Usage = Normalize("codex", "m", json.RawMessage(`{"total":{"inputTokens":900,"cachedInputTokens":0,"outputTokens":20}}`), "")
	c := a
	c.Scope, c.Status = "two", "completed"
	c.Usage = Normalize("codex", "m", nil, "")
	total, groups, err := Summarize([]Attempt{a, a, b, c}, "project")
	if err != nil || total.Attempts != 3 || total.Retries != 1 || total.Failed != 1 || total.Input.Tokens != 1000 || total.Output.Tokens != 30 || total.Measured != 2 || total.Missing != 1 || len(groups) != 2 {
		t.Fatal(total, groups, err)
	}
	if total.CacheHitRate == nil || math.Abs(*total.CacheHitRate-.08) > 1e-9 || total.Input.Missing != 1 {
		t.Fatal(total)
	}
	if total.Cost.Reported != 0 || total.Cost.Missing != 3 {
		t.Fatal(total.Cost)
	}
}

func TestModelBreakdownIsAdditiveForTokensNotAttemptCounts(t *testing.T) {
	u := Normalize("claude", "", nil, envelopeFixture(t, `{"type":"result","total_cost_usd":0.1,"modelUsage":{"a":{"inputTokens":10,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"outputTokens":2,"costUSD":0.01},"b":{"inputTokens":20,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"outputTokens":3}}}`))
	total, groups, err := Summarize([]Attempt{{ID: "r", Provider: "claude", Usage: u}}, "model")
	if err != nil || total.Attempts != 1 || len(groups) != 2 || total.Input.Tokens != 30 || total.Output.Tokens != 5 || total.Cost.USD != .1 {
		t.Fatal(total, groups, err)
	}
	if groups[0].Stats.Cost.Reported != 1 || groups[1].Stats.Cost.Missing != 1 {
		t.Fatal(groups)
	}
}

func TestUsageCacheDenominatorExcludesUnpairedRecordsAndUsesUTCDay(t *testing.T) {
	rows := []Attempt{
		{ID: "paired", StartedAt: "2026-10-21T00:10:00+08:00", FinishedAt: "2026-10-21T00:10:02+08:00", Usage: Normalize("codex", "m", json.RawMessage(`{"total":{"inputTokens":100,"cachedInputTokens":80,"outputTokens":10}}`), "")},
		{ID: "unpaired", StartedAt: "2026-10-20T18:00:00Z", Usage: Normalize("codex", "m", json.RawMessage(`{"total":{"inputTokens":900,"outputTokens":20}}`), "")},
		{ID: "partial", StartedAt: "2026-10-21T00:00:00Z", Usage: Normalize("claude", "m", json.RawMessage(`{"input_tokens":9,"cache_read_input_tokens":30,"output_tokens":2}`), "")},
	}
	total, groups, err := Summarize(rows, "day")
	if err != nil || total.Input.Tokens != 1000 || total.CacheInput != 100 || total.CacheHits != 80 || total.CacheHitRate == nil || *total.CacheHitRate != .8 || total.Partial != 1 || total.Measured != 2 {
		t.Fatal(total, err)
	}
	if len(groups) != 2 || groups[0].Key != "2026-10-20" || groups[0].Stats.Attempts != 2 || total.DurationMS != 2000 || total.TimedAttempts != 1 {
		t.Fatal(total, groups)
	}
	zero := Normalize("codex", "m", json.RawMessage(`{"total":{"inputTokens":0,"cachedInputTokens":0,"outputTokens":0}}`), "")
	total, _, err = Summarize([]Attempt{{ID: "zero", Usage: zero}}, "model")
	if err != nil || total.Measured != 1 || total.Input.Reported != 1 || total.CacheHitRate != nil {
		t.Fatal(total, err)
	}
}

func TestUsageRejectsOverflowAndMalformedNestedCounters(t *testing.T) {
	u := Normalize("codex", "m", json.RawMessage(`{"total":{"inputTokens":9223372036854775807,"outputTokens":1}}`), "")
	if _, _, err := Summarize([]Attempt{{ID: "one", Usage: u}, {ID: "two", Usage: u}}, "task"); err == nil {
		t.Fatal("overflow silently wrapped")
	}
	if _, _, err := Summarize(nil, "bad"); err == nil {
		t.Fatal("invalid empty grouping accepted")
	}
	u = Normalize("claude", "m", nil, envelopeFixture(t, `{"type":"result","modelUsage":{"bad":"not counters","good":{"inputTokens":2,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"outputTokens":1}}}`))
	total, groups, err := Summarize([]Attempt{{ID: "partial", Usage: u}}, "model")
	if err != nil || len(groups) != 2 || total.Partial != 1 || total.Input.Tokens != 2 || total.Input.Missing != 1 || total.Cost.Reported != 0 {
		t.Fatal(total, groups, err)
	}
	// A malformed JSON receipt cannot contribute partial fields decoded before
	// its type error, even if it happens to contain valid-looking usage.
	u = Normalize("claude", "m", nil, `{"visible":"{\"type\":\"result\",\"total_cost_usd\":100}","usage":invalid}`)
	if u.EstimatedUSD != nil {
		t.Fatal("malformed envelope produced estimated cost", u)
	}
}
