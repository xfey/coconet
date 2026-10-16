package workgraph

import (
	"errors"
	"sort"
)

const MaxRetiredSources = 4096

func applyRetirement(state *State, commit Commit) error {
	sources := commit.Retire.Sources
	if len(sources) == 0 || len(sources) > MaxRetiredSources {
		return errors.New("retirement requires a bounded nonempty source set")
	}
	retained := RetainedSources(*state)
	seen := map[string]bool{}
	for _, source := range sources {
		snapshot, exists := state.Snapshots[source.Key()]
		if !exists || seen[source.Key()] {
			return ErrSource
		}
		if snapshot.Owner != commit.Actor {
			return ErrPermission
		}
		if retained[source.Key()] {
			return errors.New("cannot retire a referenced or unconsumed source")
		}
		seen[source.Key()] = true
	}
	for key := range seen {
		delete(state.Snapshots, key)
	}
	return nil
}

// RetirementCandidates must be called with durable task/proposal/lease roots
// supplied by the publisher. Metadata retirement is distinct from physical
// object deletion; the latter also needs Relay leases and a grace period.
func RetirementCandidates(state State, actor string, protected map[string]bool) []Source {
	retained := RetainedSources(state)
	candidates := []Source{}
	for key, snapshot := range state.Snapshots {
		if snapshot.Owner == actor && !retained[key] && !protected[key] {
			candidates = append(candidates, snapshot.Source)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].SessionID == candidates[j].SessionID {
			return candidates[i].Version < candidates[j].Version
		}
		return candidates[i].SessionID < candidates[j].SessionID
	})
	if len(candidates) > MaxRetiredSources {
		candidates = candidates[:MaxRetiredSources]
	}
	return candidates
}
