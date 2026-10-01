package sourceblob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/contextio"
)

// CopySHA streams a verified object without buffering its compressed or plain body.
// Verification completes after the final destination write.
func (s *Store) CopySHA(ctx context.Context, sha string, dst io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil {
		return os.ErrNotExist
	}
	rel, err := RelPath(sha)
	if err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	path, err := s.resolve(rel)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	decoder, err := acquireDecoder(f)
	if err != nil {
		return err
	}
	defer releaseDecoder(decoder)
	digest := sha256.New()
	if _, err := io.Copy(io.MultiWriter(dst, digest), contextio.Reader{Context: ctx, Source: decoder}); err != nil {
		return err
	}
	if hex.EncodeToString(digest.Sum(nil)) != sha {
		return fmt.Errorf("content sha mismatch")
	}
	return ctx.Err()
}
