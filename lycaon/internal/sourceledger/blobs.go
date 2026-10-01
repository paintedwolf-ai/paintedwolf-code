package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"os"

	"github.com/lycaon/lycaon/internal/sourceblob"
)

// readVerifiedBlob reports missing or hash-mismatched content as not found.
func (s *Store) readVerifiedBlob(ctx context.Context, sha256 string) ([]byte, bool, error) {
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
