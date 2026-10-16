package graphstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xfey/coconet/packages/conversation"
)

// WalkConversation validates a fixed text closure without materializing an
// export file or retaining the full transcript in memory.
func (s *Store) WalkConversation(ctx context.Context, digest string, turns int, visit func(conversation.Entry) error) error {
	release := s.PinContent()
	defer release()
	data, err := s.Object(digest)
	if err != nil {
		return err
	}
	var manifest conversation.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	if manifest.Count != turns {
		return errors.New("conversation differs from fixed source")
	}
	return conversation.Walk(manifest, func(block conversation.Block) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return s.Object(block.SHA256)
	}, visit)
}
