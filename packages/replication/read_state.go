package replication

import (
	"context"
	"errors"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/workgraph"
)

// StateReader reads graph metadata only; it cannot request Session objects.
type StateReader interface {
	Checkpoint(context.Context, string) (contracts.RelayCheckpointPage, error)
	Since(context.Context, string, int64) (contracts.RelayPage, error)
}

// ReadState verifies a published checkpoint and replays its accepted tail using
// the same decoder/reducer as device replication. It reads to a fixed head, so a
// busy project cannot keep an explicit browser edit chasing new commits forever.
func ReadState(ctx context.Context, remote StateReader, project string) (workgraph.State, error) {
	page, err := remote.Checkpoint(ctx, project)
	if err != nil {
		return workgraph.State{}, err
	}
	if page.SchemaVersion != 1 || page.Head < 0 {
		return workgraph.State{}, errors.New("invalid checkpoint page")
	}
	state := workgraph.New(project)
	if page.Checkpoint != nil {
		state, _, err = decodeCheckpoint(project, *page.Checkpoint)
		if err != nil {
			return state, err
		}
	}
	if state.Revision > page.Head {
		return state, errors.New("checkpoint is ahead of Relay")
	}
	for reads := 0; state.Revision < page.Head; reads++ {
		if err := ctx.Err(); err != nil {
			return state, err
		}
		if reads >= 64 {
			return state, ErrConflict
		}
		tail, err := remote.Since(ctx, project, state.Revision)
		if err != nil {
			return state, err
		}
		if tail.SchemaVersion != 1 || tail.Head < page.Head || len(tail.Entries) == 0 {
			return state, errors.New("Relay omitted required commits")
		}
		for _, receipt := range tail.Entries {
			if state.Revision == page.Head {
				break
			}
			if receipt.Revision != state.Revision+1 || receipt.Revision > tail.Head {
				return state, errors.New("Relay commit sequence is not contiguous")
			}
			commit, err := decodeReceipt(project, receipt)
			if err != nil {
				return state, err
			}
			next, change, err := workgraph.Apply(state, commit)
			if err != nil {
				return state, err
			}
			if change.StateDigest != receipt.Commit.StateSHA256 {
				return state, errors.New("accepted state differs from Relay anchor")
			}
			state = next
		}
	}
	if state.Revision > 0 {
		err = workgraph.ValidateState(state)
	}
	return state, err
}
