package main

import (
	"github.com/xfey/coconet/packages/contracts"
)

type historyBatchRecord struct {
	GraphRetryCount   int                                `json:"graph_retry_count,omitempty"`
	GraphInvalidCount int                                `json:"graph_invalid_count,omitempty"`
	Patch             *historyPatchRecord                `json:"patch,omitempty"`
	Bootstrap         *bootstrapBuildRecord              `json:"bootstrap,omitempty"`
	Manifest          contracts.BeginHistoryBatchRequest `json:"manifest"`
	Batch             contracts.HistoryBatch             `json:"batch"`
}

func legacyProjectProjection(projectID string) contracts.ProjectProjectionState {
	return contracts.ProjectProjectionState{ProjectID: projectID, Mode: "legacy", State: "incremental"}
}

func migrateHistoryBatches(state *databaseState) {
	for projectID := range state.Projects.All() {
		if exists := state.ProjectProjections.Has(projectID); !exists {
			state.ProjectProjections.Set(projectID, legacyProjectProjection(projectID))
		}
	}
}
