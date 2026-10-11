package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

// CopyRecoveryFile verifies the complete retained object while streaming it.
func (s *Retention) CopyRecoveryFile(ctx context.Context, sha string, destination io.Writer) error {
	return s.objects.CopySHA(ctx, sha, destination)
}

// readVerifiedBlob reports missing or hash-mismatched content as not found.
func (s *Retention) readVerifiedBlob(ctx context.Context, sha256 string) ([]byte, bool, error) {
	if s == nil || sha256 == "" {
		return nil, false, nil
	}
	object, err := s.queries.GetSourceBlobObject(ctx, sha256)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	raw, err := s.objects.Get(object.StorageRelpath)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if sourceblob.ContentSHA(raw) != sha256 {
		return nil, false, nil
	}
	return raw, true, nil
}

// Retention manages recoverable content and reference-aware collection.
type Retention struct {
	baselines *workspacebaseline.Store
	objects   *sourceblob.Store
	queries   *db.Queries
	snapshots *sourcesnapshot.Store
	sqlDB     db.Handle
}
