package modelusage

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Attempt contains only usage metadata. No prompts, outputs or native paths
// cross the local reporting endpoint.
type Attempt struct {
	ID         string `json:"id"`
	TaskID     string `json:"task_id"`
	ProjectID  string `json:"project_id"`
	Scope      string `json:"scope,omitempty"`
	Provider   string `json:"provider"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	Retry      bool   `json:"retry"`
	Usage      Record `json:"usage"`
}

type Counter struct {
	Tokens   int64 `json:"reported_tokens"`
	Reported int   `json:"reported_model_records"`
	Missing  int   `json:"missing_model_records"`
}

type Cost struct {
	USD      float64 `json:"reported_usd"`
	Reported int     `json:"reported_attempts"`
	Missing  int     `json:"missing_attempts"`
}

type Stats struct {
	Attempts      int      `json:"attempts"`
	Retries       int      `json:"retry_attempts"`
	Failed        int      `json:"failed_attempts"`
	Running       int      `json:"running_attempts"`
	Measured      int      `json:"measured_attempts"`
	Partial       int      `json:"partial_attempts"`
	Missing       int      `json:"missing_attempts"`
	Input         Counter  `json:"input"`
	Uncached      Counter  `json:"uncached_input"`
	Output        Counter  `json:"output"`
	CacheRead     Counter  `json:"cache_read_input"`
	CacheWrite    Counter  `json:"cache_write_input"`
	Reasoning     Counter  `json:"reasoning_output"`
	Cost          Cost     `json:"cli_estimated_cost"`
	CacheHitRate  *float64 `json:"cache_read_hit_rate"`
	CacheInput    int64    `json:"cache_measured_input_tokens"`
	CacheHits     int64    `json:"cache_measured_read_tokens"`
	DurationMS    int64    `json:"completed_duration_ms"`
	TimedAttempts int      `json:"timed_attempts"`
}

type Group struct {
	Key      string `json:"key"`
	Scope    string `json:"scope,omitempty"`
	Provider string `json:"provider,omitempty"`
	Stats    Stats  `json:"stats"`
}

func (c *Counter) add(value *int64) error {
	if value == nil {
		c.Missing++
		return nil
	}
	if *value < 0 || *value > math.MaxInt64-c.Tokens {
		return fmt.Errorf("token counter exceeds the supported integer range")
	}
	c.Tokens += *value
	c.Reported++
	return nil
}

func (s *Stats) add(a Attempt, models []Model, cost *float64) error {
	s.Attempts++
	if a.Retry {
		s.Retries++
	}
	if a.Status == "failed" || a.Status == "interrupted" {
		s.Failed++
	}
	if a.Status == "running" {
		s.Running++
	}
	complete, any := len(models) > 0, false
	for _, m := range models {
		t := m.Tokens
		complete = complete && t.Input != nil && t.Output != nil
		for _, pair := range []struct {
			counter *Counter
			value   *int64
		}{
			{&s.Input, t.Input}, {&s.Uncached, t.Uncached}, {&s.Output, t.Output},
			{&s.CacheRead, t.CacheRead}, {&s.CacheWrite, t.CacheWrite}, {&s.Reasoning, t.Reasoning},
		} {
			any = any || pair.value != nil
			if err := pair.counter.add(pair.value); err != nil {
				return err
			}
		}
		if t.Input != nil && t.CacheRead != nil {
			if *t.Input > math.MaxInt64-s.CacheInput || *t.CacheRead > math.MaxInt64-s.CacheHits {
				return fmt.Errorf("cache counters exceed the supported integer range")
			}
			s.CacheInput += *t.Input
			s.CacheHits += *t.CacheRead
		}
	}
	if complete {
		s.Measured++
	} else if any {
		s.Partial++
	} else {
		s.Missing++
	}
	if s.CacheInput > 0 {
		rate := float64(s.CacheHits) / float64(s.CacheInput)
		s.CacheHitRate = &rate
	}
	if cost == nil {
		s.Cost.Missing++
	} else {
		value := s.Cost.USD + *cost
		if *cost < 0 || math.IsInf(value, 0) || math.IsNaN(value) {
			return fmt.Errorf("reported cost exceeds the supported numeric range")
		}
		s.Cost.USD = value
		s.Cost.Reported++
	}
	start, e1 := time.Parse(time.RFC3339Nano, a.StartedAt)
	end, e2 := time.Parse(time.RFC3339Nano, a.FinishedAt)
	if e1 == nil && e2 == nil && !end.Before(start) {
		duration := end.Sub(start).Milliseconds()
		if duration <= math.MaxInt64-s.DurationMS {
			s.DurationMS += duration
			s.TimedAttempts++
		}
	}
	return nil
}

// Summarize deduplicates physical attempts. Separate retries and auxiliary
// model usage remain included even when they lead to only one accepted node.
func Summarize(attempts []Attempt, groupBy string) (Stats, []Group, error) {
	if groupBy != "project" && groupBy != "model" && groupBy != "task" && groupBy != "day" {
		return Stats{}, nil, fmt.Errorf("unknown usage grouping %q", groupBy)
	}
	var total Stats
	groups := map[string]*Group{}
	seen := map[string]bool{}
	addGroup := func(a Attempt, key, scope, provider string, models []Model, cost *float64) error {
		identity := scope + "\x00" + provider + "\x00" + key
		group := groups[identity]
		if group == nil {
			group = &Group{Key: key, Scope: scope, Provider: provider}
			groups[identity] = group
		}
		return group.Stats.add(a, models, cost)
	}
	for _, a := range attempts {
		id := a.Scope + "\x00" + a.ID
		if seen[id] {
			continue
		}
		seen[id] = true
		if err := total.add(a, a.Usage.Models, a.Usage.EstimatedUSD); err != nil {
			return Stats{}, nil, err
		}
		var err error
		switch groupBy {
		case "project":
			err = addGroup(a, a.ProjectID, a.Scope, "", a.Usage.Models, a.Usage.EstimatedUSD)
		case "model":
			for _, m := range a.Usage.Models {
				if err = addGroup(a, m.Name, "", a.Provider, []Model{m}, m.EstimatedUSD); err != nil {
					break
				}
			}
		case "task":
			err = addGroup(a, a.Kind, "", "", a.Usage.Models, a.Usage.EstimatedUSD)
		case "day":
			date := "unknown"
			if t, e := time.Parse(time.RFC3339Nano, a.StartedAt); e == nil {
				date = t.UTC().Format("2006-01-02")
			}
			err = addGroup(a, date, "", "", a.Usage.Models, a.Usage.EstimatedUSD)
		}
		if err != nil {
			return Stats{}, nil, err
		}
	}
	result := make([]Group, 0, len(groups))
	for _, group := range groups {
		result = append(result, *group)
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		return a.Scope < b.Scope
	})
	return total, result, nil
}
