package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/packages/contracts"
)

var (
	symbolPattern = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
)

func mergeCumulativeTurns(base, incoming []contracts.NormalizedTurn) ([]contracts.NormalizedTurn, []contracts.NormalizedTurn) {
	result := append([]contracts.NormalizedTurn(nil), base...)
	exact := make(map[string]bool, len(result))
	usedIDs := make(map[string]bool, len(result))
	for _, turn := range result {
		exact[cumulativeSourceTurnID(turn)+"\x00"+turn.ContentHash] = true
		usedIDs[turn.TurnID] = true
	}
	delta := make([]contracts.NormalizedTurn, 0, len(incoming))
	for _, turn := range incoming {
		sourceTurnID := cumulativeSourceTurnID(turn)
		if exact[sourceTurnID+"\x00"+turn.ContentHash] {
			continue
		}
		if usedIDs[turn.TurnID] {
			turn.SourceTurnID = sourceTurnID
			turn.TurnID = cumulativeRevisionID(turn.TurnID, turn.ContentHash, usedIDs)
		}
		turn.Ordinal = len(result)
		result = append(result, turn)
		delta = append(delta, turn)
		exact[sourceTurnID+"\x00"+turn.ContentHash] = true
		usedIDs[turn.TurnID] = true
	}
	return result, delta
}

func cumulativeSourceTurnID(turn contracts.NormalizedTurn) string {
	if turn.SourceTurnID != "" {
		return turn.SourceTurnID
	}
	return turn.TurnID
}

func cumulativeRevisionID(sourceID, contentHash string, used map[string]bool) string {
	suffix := contentHash
	if len(suffix) > 12 {
		suffix = suffix[:12]
	}
	candidate := sourceID + "@" + suffix
	if len(candidate) > 500 {
		candidate = "turn_" + adapters.Digest(sourceID + "\x00" + contentHash)[:32]
	}
	for index := 2; used[candidate]; index++ {
		candidate = fmt.Sprintf("turn_%s_%d", adapters.Digest(sourceID + "\x00" + contentHash)[:24], index)
	}
	return candidate
}

func reconstructNormalizedTurns(state databaseState, serverSessionID string, version int) ([]contracts.NormalizedTurn, error) {
	if version <= 0 {
		return []contracts.NormalizedTurn{}, nil
	}
	if record, ok := state.snapshotContents().Lookup(versionKey(serverSessionID, version)); ok && record.Content != nil {
		entries, err := recordConversation(state, record, false)
		if err != nil {
			return nil, err
		}
		return conversationTurns(entries), nil
	}

	chain := make([]normalizedVersionRecord, 0, version)
	seen := map[int]bool{}
	current := version
	for current > 0 {
		if seen[current] {
			return nil, errors.New("normalized index chain contains a cycle")
		}
		seen[current] = true
		record, ok := state.snapshotContents().Lookup(versionKey(serverSessionID, current))
		if !ok {
			return nil, notFound("session_version_not_indexed", "Session Version has no normalized index")
		}
		if record.Content != nil {
			entries, err := recordConversation(state, record, false)
			if err != nil {
				return nil, err
			}
			record.Turns = conversationTurns(entries)
			record.Summary.BaseIndexVersion, record.SourceBaseVersion = 0, 0
		}
		chain = append(chain, record)
		current = record.Summary.BaseIndexVersion
	}
	turns := []contracts.NormalizedTurn{}
	for index := len(chain) - 1; index >= 0; index-- {
		record := chain[index]
		if record.Summary.BaseIndexVersion == 0 {
			turns = append([]contracts.NormalizedTurn(nil), record.Turns...)
		} else {
			turns = append(turns, record.Turns...)
		}
	}
	if len(turns) != chain[0].Summary.TurnCount {
		return nil, errors.New("normalized index turn count is inconsistent")
	}
	return turns, nil
}

func reconstructSourceTurns(state databaseState, serverSessionID string, version int) ([]contracts.NormalizedTurn, error) {
	if version <= 0 {
		return []contracts.NormalizedTurn{}, nil
	}
	if record, ok := state.snapshotContents().Lookup(versionKey(serverSessionID, version)); ok && record.Content != nil {
		entries, err := recordConversation(state, record, true)
		if err != nil {
			return nil, err
		}
		return conversationTurns(entries), nil
	}

	chain := make([]normalizedVersionRecord, 0, version)
	seen := map[int]bool{}
	current := version
	for current > 0 {
		if seen[current] {
			return nil, errors.New("normalized source index chain contains a cycle")
		}
		seen[current] = true
		record, ok := state.snapshotContents().Lookup(versionKey(serverSessionID, current))
		if !ok {
			return nil, notFound("session_version_not_indexed", "Session Version has no normalized index")
		}
		if record.Content != nil {
			entries, err := recordConversation(state, record, true)
			if err != nil {
				return nil, err
			}
			record.SourceTurns = conversationTurns(entries)
			record.Summary.BaseIndexVersion, record.SourceBaseVersion = 0, 0
		}
		chain = append(chain, record)
		current = record.SourceBaseVersion
	}
	turns := []contracts.NormalizedTurn{}
	for index := len(chain) - 1; index >= 0; index-- {
		record := chain[index]
		if record.Reset || record.SourceBaseVersion == 0 {
			turns = append([]contracts.NormalizedTurn(nil), record.SourceTurns...)
		} else {
			turns = append(turns, record.SourceTurns...)
		}
	}
	return turns, nil
}

func hashNormalizedTurns(turns []contracts.NormalizedTurn) (string, error) {
	content, err := json.Marshal(turns)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:]), nil
}

func requireReadableMember(state *databaseState, actorID, projectID string) (contracts.Project, contracts.Membership, error) {
	project, ok := state.Projects.Lookup(projectID)
	if !ok {
		return contracts.Project{}, contracts.Membership{}, notFound("project_not_found", "Project was not found")
	}
	membership, ok := state.Memberships.Lookup(membershipKey(projectID, actorID))
	if !ok {
		return contracts.Project{}, contracts.Membership{}, forbidden("membership_required", "Project membership is required")
	}
	if project.Status == contracts.ProjectActive && membership.Status == contracts.MembershipActive {
		return project, membership, nil
	}
	if project.Status == contracts.ProjectArchived && membership.Status == contracts.MembershipArchived {
		return project, membership, nil
	}
	return contracts.Project{}, contracts.Membership{}, forbidden("membership_required", "readable Project membership is required")
}

func inferSymbols(text string) []string {
	result := []string{}
	for _, token := range symbolPattern.FindAllString(text, 300) {
		if len(token) < 3 || len(token) > 200 {
			continue
		}
		if strings.Contains(token, "_") || hasMixedCase(token) {
			result = append(result, token)
		}
	}
	return uniqueStrings(result, false)
}

func hasMixedCase(value string) bool {
	hasUpper, hasLower := false, false
	for _, current := range value {
		hasUpper = hasUpper || unicode.IsUpper(current)
		hasLower = hasLower || unicode.IsLower(current)
	}
	return hasUpper && hasLower
}

func uniqueStrings(values []string, fold bool) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		key := value
		if fold {
			key = strings.ToLower(value)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, value)
	}
	return result
}
