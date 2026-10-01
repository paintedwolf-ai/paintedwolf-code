package visual

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

const artifactPruneBatchSize = 128

type artifactPruneState struct {
	dir *os.File
}

func artifactContentHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func artifactBlobName(hash string) string {
	return strings.ToLower(strings.TrimSpace(hash))
}

func putArtifactBlob(dir, hash string, raw []byte) (bool, int64, error) {
	if artifactContentHash(raw) != hash {
		return false, 0, fmt.Errorf("artifact content hash mismatch")
	}
	name := artifactBlobName(hash)
	existing, err := fseffect.OpenRead(fseffect.Location{Root: dir, Rel: name})
	if err == nil {
		info, statErr := existing.Stat()
		verifyErr := VerifyArtifactBody(existing, hash, int64(len(raw)))
		closeErr := existing.Close()
		if statErr == nil && verifyErr == nil && closeErr == nil {
			return false, info.Size(), nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, 0, err
	}
	encoded, err := zstdcodec.Compress(bytes.NewReader(raw))
	if err != nil {
		return false, 0, err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: dir, Rel: name},
		Source:   bytes.NewReader(encoded), Mode: 0o600, DirMode: 0o700,
	})
	return err == nil, int64(len(encoded)), err
}

func readArtifactBlob(dir, hash string, byteSize int64) ([]byte, bool) {
	file, err := fseffect.OpenRead(fseffect.Location{Root: dir, Rel: artifactBlobName(hash)})
	if err != nil {
		return nil, false
	}
	defer func() { _ = file.Close() }()
	var raw bytes.Buffer
	if err := copyArtifactBody(&raw, file, hash, byteSize); err != nil {
		return nil, false
	}
	return raw.Bytes(), true
}

func removeArtifactBlob(dir, hash string) error {
	err := fseffect.Remove(fseffect.RemoveRequest{
		Location: fseffect.Location{Root: dir, Rel: artifactBlobName(hash)},
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func pruneArtifactBlobBatch(dir string, state *artifactPruneState, isOrphan func(string) (bool, error)) (bool, error) {
	if state.dir == nil {
		opened, err := os.Open(dir)
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		state.dir = opened
	}
	entries, readErr := state.dir.ReadDir(artifactPruneBatchSize)
	done := errors.Is(readErr, io.EOF) || len(entries) < artifactPruneBatchSize
	if readErr != nil && !done {
		state.close()
		return false, readErr
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		hash := strings.ToLower(entry.Name())
		orphan := true
		if len(hash) == sha256.Size*2 {
			if _, err := hex.DecodeString(hash); err == nil {
				orphan, err = isOrphan(hash)
				if err != nil {
					state.close()
					return false, err
				}
			}
		}
		if !orphan {
			continue
		}
		if err := fseffect.Remove(fseffect.RemoveRequest{
			Location: fseffect.Location{Root: dir, Rel: entry.Name()},
		}); err != nil && !errors.Is(err, os.ErrNotExist) {
			state.close()
			return false, err
		}
	}
	if done {
		state.close()
	}
	return done, nil
}

func (s *artifactPruneState) close() {
	if s.dir != nil {
		_ = s.dir.Close()
		s.dir = nil
	}
}
