package replication

import (
	"context"
	"errors"
	"net/url"

	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/graphstore"
	"github.com/xfey/coconet/packages/workgraph"
)

type ReceiptTransport interface {
	Receipt(context.Context, string, string) (contracts.RelayCommitEvidence, error)
}

func (t HTTP) Receipt(ctx context.Context, project, id string) (contracts.RelayCommitEvidence, error) {
	var result contracts.RelayCommitEvidence
	err := t.json(ctx, "GET", project, "receipts/"+url.PathEscape(id), nil, &result)
	return result, err
}

func (c *Client) recoverCheckpoint(ctx context.Context, project string, revision int64) error {
	remote, ok := c.Remote.(CheckpointTransport)
	if !ok {
		return errors.New("history recovery requires checkpoint transport")
	}
	page, err := remote.Checkpoint(ctx, project)
	if err != nil {
		return err
	}
	if page.SchemaVersion != 1 || page.Checkpoint == nil {
		return errors.New("compacted history has no checkpoint")
	}
	state, anchor, err := decodeCheckpoint(project, *page.Checkpoint)
	if err != nil {
		return err
	}
	if state.Revision <= revision || state.Revision > page.Head {
		return errors.New("history checkpoint does not advance the local revision")
	}
	pending, err := c.Store.Pending(project)
	if err != nil {
		return err
	}
	accepted := map[string]contracts.RelayCommitEvidence{}
	for _, commit := range pending {
		if commit.Base >= state.Revision {
			continue
		}
		r, ok := c.Remote.(ReceiptTransport)
		if !ok {
			return errors.New("pending publication recovery requires receipt evidence")
		}
		evidence, err := r.Receipt(ctx, project, commit.ID)
		if errors.Is(err, contracts.ErrRelayReceiptMissing) {
			continue
		}
		if err != nil {
			return err
		}
		if !graphstore.MatchesEvidence(commit, evidence) || evidence.Revision > state.Revision {
			return errors.New("pending publication receipt differs")
		}
		accepted[commit.ID] = evidence
	}
	if err := c.Store.RecoverCheckpoint(state, *page.Checkpoint, anchor, revision, accepted); err != nil {
		if errors.Is(err, workgraph.ErrConflict) {
			return nil
		} // A concurrent sync advanced first.
		return err
	}
	return c.Store.RecordTransfer(graphstore.Transfer{ProjectID: project, Operation: "checkpoint_recovery", Status: "accepted", Revision: state.Revision, CommitID: anchor.ID, SHA256: page.Checkpoint.Checkpoint.SHA256})
}
