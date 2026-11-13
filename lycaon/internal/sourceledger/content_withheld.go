package sourceledger

import (
	"context"

	"github.com/lycaon/lycaon/internal/db"
)

// SetContentWithheld installs the rule for paths whose bytes history never
// retains. Their versions record identity and size with content not captured.
func (s *Store) SetContentWithheld(fn func(ctx context.Context, projectID, rootID, rel string) bool) {
	s.withheld = fn
}

func (s *Store) contentWithheld(ctx context.Context, projectID, rootID, rel string) bool {
	return s != nil && s.withheld != nil && rootID != "" && rel != "" && s.withheld(ctx, projectID, rootID, rel)
}

// prepareContentAt stores content for a path unless its bytes are withheld.
func (s *Store) prepareContentAt(ctx context.Context, projectID, rootID, rel, sha string, content []byte, kind string) (*db.UpsertSourceBlobObjectParams, error) {
	if s.contentWithheld(ctx, projectID, rootID, rel) {
		content = nil
	}
	return prepareContent(ctx, s.Content, sha, content, kind)
}
