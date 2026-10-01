package sourceledger

import (
	"context"
	"io"
)

// CopyRecoveryFile verifies the complete retained object while streaming it.
func (s *Store) CopyRecoveryFile(ctx context.Context, sha string, destination io.Writer) error {
	return s.objects.CopySHA(ctx, sha, destination)
}
