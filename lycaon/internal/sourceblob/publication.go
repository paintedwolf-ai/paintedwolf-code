package sourceblob

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// Put stores a complete payload already held in memory.
func (s *Store) Put(ctx context.Context, sha string, plain []byte) (rel string, stored int64, oids GitOIDs, err error) {
	if s == nil {
		return "", 0, GitOIDs{}, fmt.Errorf("content store unavailable")
	}
	if err := ctx.Err(); err != nil {
		return "", 0, GitOIDs{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if ContentSHA(plain) != strings.ToLower(strings.TrimSpace(sha)) {
		return "", 0, GitOIDs{}, fmt.Errorf("content sha mismatch")
	}
	oids = ContentGitOIDs(plain)
	rel, err = RelPath(sha)
	if err != nil {
		return "", 0, GitOIDs{}, err
	}
	compressed, err := compressBytes(ctx, plain)
	if err != nil {
		return "", 0, GitOIDs{}, err
	}
	stored = int64(len(compressed))
	matched, err := s.matchesObjectBytes(ctx, rel, compressed)
	if err != nil {
		return "", 0, GitOIDs{}, err
	}
	if matched {
		return rel, stored, oids, nil
	}
	if err := s.commitBytes(ctx, rel, compressed); err != nil {
		return "", 0, GitOIDs{}, err
	}
	return rel, int64(len(compressed)), oids, nil
}

func compressBytes(ctx context.Context, plain []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Grow(len(plain) + 32)
	encoder, err := acquireEncoder(&out)
	if err != nil {
		return nil, err
	}
	defer releaseEncoder(encoder)
	if _, err := encoder.Write(plain); err != nil {
		_ = encoder.Close()
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), ctx.Err()
}

// commitBytes atomically publishes a complete object.
func (s *Store) commitBytes(ctx context.Context, rel string, compressed []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return fmt.Errorf("create content object root: %w", err)
	}
	restoreMode, err := prepareObjectPublication(filepath.Join(s.root, rel))
	if err != nil {
		return err
	}
	defer restoreMode()
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: s.root, Rel: rel},
		Source:   bytes.NewReader(compressed), Mode: 0o400, DirMode: 0o700,
		BeforeCommit: func(fseffect.Target, fseffect.Result) error { return ctx.Err() },
	})
	if err != nil {
		return fmt.Errorf("commit content object: %w", err)
	}
	return nil
}
