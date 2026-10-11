package sourceblob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/contextio"
)

// Reuse requires the complete encoded object to match.
func (s *Store) matchesObject(ctx context.Context, rel string, size int64, sha string) (bool, error) {
	file, err := os.Open(filepath.Join(s.root, rel))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return false, nil
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, contextio.Reader{Context: ctx, Source: file}); err != nil {
		return false, err
	}
	return hex.EncodeToString(digest.Sum(nil)) == sha, ctx.Err()
}
