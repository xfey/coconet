package main

import (
	"errors"
	"slices"

	"github.com/xfey/coconet/packages/contracts"
)

// Identity follows cumulative history deduplication, including native revisions.
// A pair avoids allocating a concatenated key for every evidence scan.
type visibleTextIdentity struct{ turnID, contentHash string }

func visibleTurnKey(turn contracts.NormalizedTurn) visibleTextIdentity {
	return visibleTextIdentity{cumulativeSourceTurnID(turn), turn.ContentHash}
}

// The sidecar follows the immutable cumulative index chain, not the current
// native source chain: native compaction must not erase earlier dialogue.
func reconstructVisibleText(state databaseState, sessionID string, version int) (map[visibleTextIdentity][]contracts.TextRange, error) {
	if record, ok := state.snapshotContents().Lookup(versionKey(sessionID, version)); ok && record.Content != nil {
		entries, err := recordConversation(state, record, false)
		if err != nil {
			return nil, err
		}
		result := map[visibleTextIdentity][]contracts.TextRange{}
		for _, entry := range entries {
			if entry.Visible != nil {
				result[visibleTurnKey(entry.Turn)] = slices.Clone(entry.Ranges)
			}
		}
		return result, nil
	}

	result := make(map[visibleTextIdentity][]contracts.TextRange)
	for version > 0 {
		record, ok := state.snapshotContents().Lookup(versionKey(sessionID, version))
		if !ok || record.Summary.BaseIndexVersion >= version {
			return nil, errors.New("invalid visible text index chain")
		}
		if record.Content != nil {
			entries, err := recordConversation(state, record, false)
			if err != nil {
				return nil, err
			}
			for _, entry := range entries {
				key := visibleTurnKey(entry.Turn)
				if _, known := result[key]; !known && entry.Visible != nil {
					result[key] = slices.Clone(entry.Ranges)
				}
			}
			break
		}
		for _, turn := range record.VisibleTextDelta {
			key, ranges := visibleTextIdentity{turn.TurnID, turn.ContentHash}, turn.Ranges
			if _, exists := result[key]; !exists {
				result[key] = ranges
			}
		}
		version = record.Summary.BaseIndexVersion
	}
	return result, nil
}
