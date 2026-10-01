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

// Deduplication compares stored bytes with the freshly encoded capture.
func (s *Store) matchesCapture(ctx context.Context, rel string, staged stagedFile) (bool, error) {
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
	if !info.Mode().IsRegular() || info.Size() != staged.stored {
		return false, nil
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, contextio.Reader{Context: ctx, Source: file}); err != nil {
		return false, err
	}
	return hex.EncodeToString(digest.Sum(nil)) == staged.storedSHA, ctx.Err()
}
