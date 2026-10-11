package sourceblob

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

// Put stores a complete payload already held in memory.
func (s *Store) Put(sha string, plain []byte) (rel string, stored int64, oids GitOIDs, err error) {
	if s == nil {
		return "", 0, GitOIDs{}, fmt.Errorf("content store unavailable")
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
	compressed, err := zstdcodec.Compress(bytes.NewReader(plain))
	if err != nil {
		return "", 0, GitOIDs{}, err
	}
	stored = int64(len(compressed))
	matched, err := s.matchesObject(context.Background(), rel, stored, ContentSHA(compressed))
	if err != nil {
		return "", 0, GitOIDs{}, err
	}
	if matched {
		return rel, stored, oids, nil
	}
	if err := s.commitBytes(rel, compressed); err != nil {
		return "", 0, GitOIDs{}, err
	}
	return rel, int64(len(compressed)), oids, nil
}

// commitBytes atomically publishes a complete object.
func (s *Store) commitBytes(rel string, compressed []byte) error {
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return fmt.Errorf("create content object directory: %w", err)
	}
	restoreMode, err := prepareObjectPublication(filepath.Join(s.root, rel))
	if err != nil {
		return err
	}
	defer restoreMode()
	if _, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: s.root, Rel: rel},
		Source:   bytes.NewReader(compressed),
		Mode:     0o400,
		DirMode:  0o700,
	}); err != nil {
		return fmt.Errorf("commit content object: %w", err)
	}
	return nil
}
