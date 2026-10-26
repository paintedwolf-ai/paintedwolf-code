package sourceblob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/fssync"
)

var putTempSeq atomic.Uint64

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
	dest := filepath.Join(s.root, rel)
	dir := filepath.Dir(dest)
	tempPath := filepath.Join(dir, ".put-"+strconv.FormatUint(putTempSeq.Add(1), 36))
	openMode := os.FileMode(0o400)
	if runtime.GOOS == "windows" {
		openMode = 0o600
	}
	temp, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, openMode)
	if os.IsNotExist(err) {
		if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
			return fmt.Errorf("create content object directory: %w", mkErr)
		}
		temp, err = os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, openMode)
	}
	if err != nil {
		return fmt.Errorf("create content object temp: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	_, writeErr := temp.Write(compressed)
	var chmodErr error
	if runtime.GOOS == "windows" {
		chmodErr = temp.Chmod(0o400)
	}
	syncErr := fssync.File(temp)
	closeErr := temp.Close()
	if err := errors.Join(writeErr, chmodErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("write content object: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	restoreMode, err := prepareObjectPublication(dest)
	if err != nil {
		return err
	}
	defer restoreMode()
	if err := os.Rename(tempPath, dest); err != nil {
		return fmt.Errorf("commit content object: %w", err)
	}
	committed = true
	return syncDir(dir)
}
