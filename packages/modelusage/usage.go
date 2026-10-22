// Package modelusage normalizes reported model counters without estimating
// missing tokens or treating subscription usage as an actual invoice.
package modelusage

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
)

type Tokens struct {
	Input      *int64 `json:"input,omitempty"` // Includes cache reads and writes.
	Uncached   *int64 `json:"uncached_input,omitempty"`
	CacheRead  *int64 `json:"cache_read_input,omitempty"`
	CacheWrite *int64 `json:"cache_write_input,omitempty"`
	Output     *int64 `json:"output,omitempty"`
	Reasoning  *int64 `json:"reasoning_output,omitempty"` // Subset, never added to output.
}

type Model struct {
	Name         string   `json:"model"`
	Tokens       Tokens   `json:"tokens"`
	EstimatedUSD *float64 `json:"cli_estimated_usd,omitempty"`
}

type Record struct {
	Schema       int      `json:"schema"`
	Models       []Model  `json:"models"`
	EstimatedUSD *float64 `json:"cli_estimated_usd,omitempty"`
}

// Normalize also understands archived inference.Response envelopes, allowing
// old retained runs to contribute without replaying a model or exposing text.
func Normalize(provider, model string, usage json.RawMessage, output string) Record {
	result := Record{Schema: 1, Models: []Model{}}
	var response struct {
		Visible string          `json:"visible"`
		Usage   json.RawMessage `json:"usage"`
	}
	var visible string
	if json.Unmarshal([]byte(output), &response) == nil {
		visible = response.Visible
		if len(usage) == 0 {
			usage = response.Usage
		}
	}
	envelope := resultEnvelope(visible)
	if len(usage) == 0 || string(usage) == "null" {
		usage = envelope["usage"]
	}
	result.EstimatedUSD = amount(envelope, "total_cost_usd")
	if result.EstimatedUSD == nil {
		result.EstimatedUSD = amount(envelope, "cost_usd")
	}
	// Claude's top-level usage can describe only the main request. Per-model
	// usage includes auxiliary calls; use it instead, never add both together.
	if provider == "claude" || provider == "cursor" {
		models := object(envelope["modelUsage"])
		names := make([]string, 0, len(models))
		for name := range models {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			m := object(models[name])
			result.Models = append(result.Models, Model{Name: name, Tokens: anthropic(m, true), EstimatedUSD: amount(m, "costUSD")})
		}
	}
	if len(result.Models) == 0 {
		if strings.TrimSpace(model) == "" {
			model = "unknown"
		}
		u := object(usage)
		var tokens Tokens
		switch {
		case provider == "codex":
			// Each inference uses a new thread. total is cumulative, so summing
			// notifications (or adding last to total) would double count.
			if nested := object(u["total"]); nested != nil {
				u = nested
			} else if nested := object(u["last"]); nested != nil {
				u = nested
			}
			tokens = Tokens{Input: number(u, "inputTokens"), CacheRead: number(u, "cachedInputTokens"), CacheWrite: number(u, "cacheWriteInputTokens"), Output: number(u, "outputTokens"), Reasoning: number(u, "reasoningOutputTokens")}
			// Do not invent an unreported cache-write count on a host that
			// cannot distinguish written input from ordinary input.
			completeInput(&tokens)
		case u["prompt_tokens"] != nil:
			details := object(u["prompt_tokens_details"])
			tokens = Tokens{Input: number(u, "prompt_tokens"), Output: number(u, "completion_tokens"), CacheRead: number(details, "cached_tokens"), CacheWrite: number(details, "cache_write_tokens"), Reasoning: number(object(u["completion_tokens_details"]), "reasoning_tokens")}
			completeInput(&tokens)
		case u["input_tokens_details"] != nil:
			details := object(u["input_tokens_details"])
			tokens = Tokens{Input: number(u, "input_tokens"), Output: number(u, "output_tokens"), CacheRead: number(details, "cached_tokens"), CacheWrite: number(details, "cache_write_tokens"), Reasoning: number(object(u["output_tokens_details"]), "reasoning_tokens")}
			completeInput(&tokens)
		case provider == "claude" || provider == "cursor":
			tokens = anthropic(u, false)
		}
		result.Models = append(result.Models, Model{Name: model, Tokens: tokens, EstimatedUSD: result.EstimatedUSD})
	}
	if result.EstimatedUSD == nil {
		total, complete := 0.0, true
		for _, m := range result.Models {
			if m.EstimatedUSD == nil {
				complete = false
				break
			}
			total += *m.EstimatedUSD
		}
		if complete && !math.IsInf(total, 0) {
			result.EstimatedUSD = &total
		}
	}
	return result
}

func anthropic(u map[string]json.RawMessage, camel bool) Tokens {
	in, out, read, write := "input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"
	if camel {
		in, out, read, write = "inputTokens", "outputTokens", "cacheReadInputTokens", "cacheCreationInputTokens"
	}
	t := Tokens{Uncached: number(u, in), Output: number(u, out), CacheRead: number(u, read), CacheWrite: number(u, write)}
	if camel {
		t.Reasoning = number(u, "thinkingTokens")
	} else {
		t.Reasoning = number(object(u["output_tokens_details"]), "thinking_tokens")
	}
	t.Input = add(t.Uncached, t.CacheRead, t.CacheWrite)
	return t
}

func completeInput(t *Tokens) {
	if t.Input == nil {
		return
	}
	if t.CacheRead != nil && *t.CacheRead > *t.Input {
		t.CacheRead = nil
	}
	if t.CacheWrite != nil && *t.CacheWrite > *t.Input {
		t.CacheWrite = nil
	}
	if caches := add(t.CacheRead, t.CacheWrite); caches != nil {
		if *caches > *t.Input {
			t.CacheRead, t.CacheWrite = nil, nil
		} else {
			value := *t.Input - *caches
			t.Uncached = &value
		}
	}
}

func add(values ...*int64) *int64 {
	total := int64(0)
	for _, v := range values {
		if v == nil || *v > math.MaxInt64-total {
			return nil
		}
		total += *v
	}
	return &total
}

func number(u map[string]json.RawMessage, key string) *int64 {
	var value int64
	raw := u[key]
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &value) != nil || value < 0 {
		return nil
	}
	return &value
}

func amount(u map[string]json.RawMessage, key string) *float64 {
	var value float64
	raw := u[key]
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &value) != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
}

func object(raw json.RawMessage) map[string]json.RawMessage {
	var result map[string]json.RawMessage
	if json.Unmarshal(raw, &result) != nil {
		return nil
	}
	return result
}

func resultEnvelope(raw string) map[string]json.RawMessage {
	for raw = strings.TrimSpace(raw); raw != ""; raw = strings.TrimSpace(raw) {
		if raw[0] == '{' || raw[0] == '[' {
			u := object(json.RawMessage(raw))
			if string(u["type"]) == `"result"` {
				return u
			}
			return nil
		}
		_, raw, _ = strings.Cut(raw, "\n")
	}
	return nil
}
