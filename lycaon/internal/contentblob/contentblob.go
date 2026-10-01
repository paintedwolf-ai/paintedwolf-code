// Package contentblob stores model-output and evidence bodies by digest.
// Callers commit object metadata and references in the same transaction.
package contentblob

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

// Dir is the host-data-relative prefix content-addressed bodies live under.
const Dir = "model-content"

const shaHexLen = sha256.Size * 2

// ErrMissing reports a digest whose object file is gone. A damaged file is a
// different error.
var ErrMissing = errors.New("content blob file is missing")

// StoreFor resolves content storage under the project's host data directory.
func StoreFor(dataDir, projectID string) blobstore.Store {
	return blobstore.Store{Root: project.HostDataDir(dataDir, projectID), Dir: Dir}
}

// RelPath validates a digest and uses its first two characters as the shard.
func RelPath(sha string) (string, error) {
	sha = strings.ToLower(strings.TrimSpace(sha))
	if len(sha) != shaHexLen {
		return "", fmt.Errorf("contentblob: invalid digest %q", sha)
	}
	if _, err := hex.DecodeString(sha); err != nil {
		return "", fmt.Errorf("contentblob: invalid digest %q: %w", sha, err)
	}
	return filepath.Join(Dir, sha[:2], sha[2:]), nil
}

// Write returns the digest, plaintext size, and compressed size.
// Existing objects with the same digest are reused.
func Write(store blobstore.Store, plaintext []byte) (sha string, byteSize, storedSize int64, err error) {
	if !store.Available() {
		return "", 0, 0, blobstore.ErrNoStore
	}
	sum := sha256.Sum256(plaintext)
	sha = hex.EncodeToString(sum[:])
	rel, err := RelPath(sha)
	if err != nil {
		return "", 0, 0, err
	}
	byteSize = int64(len(plaintext))
	abs := filepath.Join(store.Root, filepath.FromSlash(rel))
	if info, statErr := os.Stat(abs); statErr == nil {
		return sha, byteSize, info.Size(), nil
	}
	limit := bytebound.Materialization(byteSize)
	if limit <= 0 {
		limit = 1
	}
	if _, err := store.PutAt(rel, bytes.NewReader(plaintext), limit); err != nil {
		return "", 0, 0, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", 0, 0, fmt.Errorf("contentblob: stat written blob: %w", err)
	}
	return sha, byteSize, info.Size(), nil
}

// Read returns the decompressed bytes of one digest.
func Read(store blobstore.Store, sha string) ([]byte, error) {
	if !store.Available() {
		return nil, blobstore.ErrNoStore
	}
	rel, err := RelPath(sha)
	if err != nil {
		return nil, err
	}
	abs := filepath.Join(store.Root, filepath.FromSlash(rel))
	raw, err := os.ReadFile(abs)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrMissing, sha)
	}
	if err != nil {
		return nil, err
	}
	return zstdcodec.Decompress(raw)
}
