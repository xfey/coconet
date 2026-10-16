package main

import (
	"time"

	"github.com/xfey/coconet/packages/contracts"
)

func laterTimestamp(left, right string) string {
	leftTime, leftErr := time.Parse(time.RFC3339Nano, left)
	rightTime, rightErr := time.Parse(time.RFC3339Nano, right)
	if rightErr != nil || leftErr == nil && leftTime.After(rightTime) {
		return left
	}
	return right
}

func sealCurrentWorkNode(state *databaseState, serverSessionID, now string) {
	currentID := state.currentNodes().Get(serverSessionID)
	if currentID == "" {
		return
	}
	node, ok := state.WorkNodes.Lookup(currentID)
	if !ok {
		state.currentNodes().Delete(serverSessionID)
		return
	}
	node.State = contracts.WorkNodeSealed
	node.UpdatedAt = now
	setWorkNode(state, node)
	recordCheckpointActivity(state, node, now, false)
	state.currentNodes().Delete(serverSessionID)
}
