// Package blobstore stores project-host bodies.
package blobstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fssync"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

// ErrNoStore means the project host data directory is unavailable.
var ErrNoStore = errors.New("blobstore: no project host data dir")

// TooLargeError reports a body over its materialization bound.
type TooLargeError struct {
	Name  string
	Limit bytebound.Materialization
}

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("blobstore: %q exceeds the materialization bound of %d bytes", e.Name, e.Limit.Int64())
}

// Blob is a materialized body.
type Blob struct {
	// ID binds the content digest to its sanitized filename.
	ID string
	// Rel is the slash-separated host-data path.
	Rel string
	// Name is the sanitized leaf filename.
	Name string
	// Size is the materialized byte count.
	Size int64
}

// Store writes under Root, below the wire-visible prefix Dir.
type Store struct {
	// Root is the absolute project host data dir.
	Root string
	// Dir is the host-data-relative prefix.
	Dir string
	// StagedTTL bounds unretained uploads.
	StagedTTL time.Duration
	// Retained reports durable claims for a blob.
	Retained func(string) (bool, error)
	// Released removes durable metadata before blob collection.
	Released func(string) error
	// MaintenanceLease defers physical collection while an archive retains bodies.
	MaintenanceLease func() (release func(), acquired bool)
}

const (
	stagingDir   = ".staging"
	retentionDir = ".retentions"
	// derivedDir holds what the host derived from a blob, inside the blob's own directory,
	// so it lives and dies with the blob. Entry lookup skips it.
	derivedDir          = ".derived"
	defaultStagingTTL   = 24 * time.Hour
	expiredPruneBatch   = 128
	retentionClaimGrace = time.Hour
	maxIdleRepairStates = 32
)

type storeState struct {
	users        int
	uploadMu     sync.Mutex
	mu           sync.Mutex
	activeMu     sync.Mutex
	active       map[string]struct{}
	contentPrune directoryCursor
	stagingPrune directoryCursor
}

type directoryCursor struct {
	dir *os.File
}

var storeStates = struct {
	sync.Mutex
	byKey map[string]*storeState
}{byKey: make(map[string]*storeState)}

func (s Store) acquireState() (*storeState, func()) {
	key := filepath.Clean(s.Root) + "\x00" + filepath.ToSlash(strings.Trim(s.Dir, "/"))
	storeStates.Lock()
	state := storeStates.byKey[key]
	if state == nil {
		state = &storeState{
			active: make(map[string]struct{}),
		}
		storeStates.byKey[key] = state
	}
	state.users++
	storeStates.Unlock()
	return state, func() { releaseStoreState(key, state) }
}

func releaseStoreState(key string, state *storeState) {
	storeStates.Lock()
	defer storeStates.Unlock()
	state.users--
	if state.users != 0 {
		return
	}
	state.mu.Lock()
	state.activeMu.Lock()
	active := len(state.active) != 0
	state.activeMu.Unlock()
	complete := state.contentPrune.dir == nil && state.stagingPrune.dir == nil
	state.mu.Unlock()
	if !active && complete && storeStates.byKey[key] == state {
		delete(storeStates.byKey, key)
		return
	}
	trimIdleRepairStates()
}

func trimIdleRepairStates() {
	idle := 0
	for _, state := range storeStates.byKey {
		if state.users == 0 && len(state.active) == 0 &&
			(state.contentPrune.dir != nil || state.stagingPrune.dir != nil) {
			idle++
		}
	}
	for key, state := range storeStates.byKey {
		if idle <= maxIdleRepairStates {
			return
		}
		if state.users != 0 || len(state.active) != 0 {
			continue
		}
		state.contentPrune.close()
		state.stagingPrune.close()
		delete(storeStates.byKey, key)
		idle--
	}
}

func (s Store) dataRoot() string {
	return filepath.Join(s.Root, filepath.FromSlash(strings.Trim(s.Dir, "/")))
}

// Available reports whether the store can materialize anything.
func (s Store) Available() bool {
	return strings.TrimSpace(s.Root) != "" && strings.TrimSpace(s.Dir) != ""
}

// RemoveAtBefore removes a path last written before cutoff.
func (s Store) RemoveAtBefore(rel string, cutoff time.Time) (bool, error) {
	if strings.TrimSpace(s.Root) == "" {
		return false, ErrNoStore
	}
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == "" || filepath.IsAbs(rel) || sandbox.HasParentTraversal(rel) {
		return false, fmt.Errorf("blobstore: invalid relative path %q", rel)
	}
	state, releaseState := s.acquireState()
	defer releaseState()
	state.mu.Lock()
	defer state.mu.Unlock()
	abs := filepath.Join(s.Root, filepath.FromSlash(rel))
	info, err := os.Stat(abs)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !cutoff.IsZero() && !info.ModTime().Before(cutoff) {
		return false, nil
	}
	if err := os.Remove(abs); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, nil
}

// Put streams r into the content-addressed layout and returns its wire path.
func (s Store) Put(name string, r io.Reader, limit bytebound.Materialization) (Blob, error) {
	if !s.Available() {
		return Blob{}, ErrNoStore
	}
	leaf := SafeName(name)
	state, releaseState := s.acquireState()
	defer releaseState()
	state.uploadMu.Lock()
	defer state.uploadMu.Unlock()
	tmpPath, contentDigest, size, err := s.stage(state, r, limit, leaf)
	if err != nil {
		return Blob{}, err
	}
	defer s.releaseStage(state, tmpPath)

	digest := attachmentID(contentDigest, leaf)
	rel := filepath.ToSlash(filepath.Join(strings.Trim(s.Dir, "/"), digest, leaf))
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := s.pruneExpiredLocked(state, time.Now()); err != nil {
		return Blob{}, err
	}
	if err := s.commitContentAddressed(tmpPath, rel, contentDigest); err != nil {
		return Blob{}, err
	}
	now := time.Now()
	_ = os.Chtimes(filepath.Dir(filepath.Join(s.Root, filepath.FromSlash(rel))), now, now)
	return Blob{ID: digest, Rel: rel, Name: leaf, Size: size}, nil
}

func attachmentID(contentDigest, leaf string) string {
	sum := sha256.Sum256([]byte(contentDigest + "\x00" + leaf))
	return hex.EncodeToString(sum[:])
}

// PutAt streams r to an explicit host-data-relative path. Callers that own their
// own naming scheme use this; content addressing is not applied.
func (s Store) PutAt(rel string, r io.Reader, limit bytebound.Materialization) (Blob, error) {
	if strings.TrimSpace(s.Root) == "" {
		return Blob{}, ErrNoStore
	}
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == "" || sandbox.HasParentTraversal(rel) {
		return Blob{}, fmt.Errorf("blobstore: %q is not a usable relative path", rel)
	}
	leaf := SafeName(rel)
	state, releaseState := s.acquireState()
	defer releaseState()
	state.uploadMu.Lock()
	defer state.uploadMu.Unlock()
	tmpPath, digest, size, err := s.stage(state, r, limit, leaf)
	if err != nil {
		return Blob{}, err
	}
	defer s.releaseStage(state, tmpPath)

	state.mu.Lock()
	defer state.mu.Unlock()
	if err := s.pruneStagingLocked(state, time.Now()); err != nil {
		return Blob{}, err
	}
	if err := s.commitReplacement(tmpPath, rel); err != nil {
		return Blob{}, err
	}
	return Blob{ID: digest, Rel: rel, Name: leaf, Size: size}, nil
}

// stage copies r into a temp file under Root, hashing as it goes. An oversized
// body is rejected without ever being committed.
func (s Store) stage(state *storeState, r io.Reader, limit bytebound.Materialization, leaf string) (tmpPath, digest string, size int64, err error) {
	if limit <= 0 {
		return "", "", 0, fmt.Errorf("blobstore: materialization bound must be positive, got %d", limit.Int64())
	}
	stageDir := filepath.Join(s.dataRoot(), stagingDir)
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return "", "", 0, fmt.Errorf("blobstore: create staging dir: %w", err)
	}
	tmp, err := os.CreateTemp(stageDir, ".blob-*")
	if err != nil {
		return "", "", 0, fmt.Errorf("blobstore: create staging file: %w", err)
	}
	tmpPath = tmp.Name()
	state.activeMu.Lock()
	state.active[tmpPath] = struct{}{}
	state.activeMu.Unlock()
	cleanup := func(cause error) (string, string, int64, error) {
		_ = tmp.Close()
		s.releaseStage(state, tmpPath)
		return "", "", 0, cause
	}
	if err := tmp.Chmod(0o600); err != nil {
		return cleanup(fmt.Errorf("blobstore: chmod staging file: %w", err))
	}
	hasher := sha256.New()
	// One byte past the bound distinguishes "exactly at the limit" from "over".
	n, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(r, limit.Int64()+1))
	if err != nil {
		return cleanup(fmt.Errorf("blobstore: copy body: %w", err))
	}
	if n > limit.Int64() {
		return cleanup(&TooLargeError{Name: leaf, Limit: limit})
	}
	// Content identity is fixed above, on the plaintext bytes; compression is
	// a storage-layer transform applied to the staged file afterward.
	if err := compressStagedFile(tmp); err != nil {
		return cleanup(fmt.Errorf("blobstore: compress staged blob: %w", err))
	}
	if err := fssync.File(tmp); err != nil {
		return cleanup(fmt.Errorf("blobstore: sync staging file: %w", err))
	}
	if err := tmp.Close(); err != nil {
		s.releaseStage(state, tmpPath)
		return "", "", 0, fmt.Errorf("blobstore: close staging file: %w", err)
	}
	return tmpPath, hex.EncodeToString(hasher.Sum(nil)), n, nil
}

// compressStagedFile rewrites tmp's plaintext contents as a zstd frame,
// in place, leaving the file positioned as os.CreateTemp left it (open,
// at end-of-file) so the caller's Sync/Close sequence is unchanged.
func compressStagedFile(tmp *os.File) error {
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind plaintext: %w", err)
	}
	compressed, err := zstdcodec.Compress(tmp)
	if err != nil {
		return fmt.Errorf("compress: %w", err)
	}
	if err := tmp.Truncate(0); err != nil {
		return fmt.Errorf("truncate plaintext: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind for compressed write: %w", err)
	}
	if _, err := tmp.Write(compressed); err != nil {
		return fmt.Errorf("write compressed body: %w", err)
	}
	return nil
}

func (s Store) releaseStage(state *storeState, path string) {
	_ = os.Remove(path)
	_ = os.Remove(filepath.Dir(path))
	state.activeMu.Lock()
	delete(state.active, path)
	state.activeMu.Unlock()
}

func (state *storeState) stageIsActive(path string) bool {
	state.activeMu.Lock()
	defer state.activeMu.Unlock()
	_, ok := state.active[path]
	return ok
}

func (s Store) prepareCommit(tmpPath, rel string) error {
	abs := filepath.Join(s.Root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return fmt.Errorf("blobstore: create blob dir: %w", err)
	}
	if _, err := os.Stat(tmpPath); err != nil {
		return fmt.Errorf("blobstore: stat staged blob: %w", err)
	}
	return nil
}

func (s Store) commitContentAddressed(tmpPath, rel, wantDigest string) error {
	if err := s.prepareCommit(tmpPath, rel); err != nil {
		return err
	}
	existing, err := fseffect.OpenRead(fseffect.Location{Root: s.Root, Rel: filepath.FromSlash(rel)})
	if err == nil {
		defer func() { _ = existing.Close() }()
		dec, err := zstd.NewReader(existing)
		if err != nil {
			return fmt.Errorf("blobstore: open existing blob decoder: %w", err)
		}
		defer dec.Close()
		h := sha256.New()
		if _, err := io.Copy(h, dec); err != nil {
			return fmt.Errorf("blobstore: verify existing blob: %w", err)
		}
		if hex.EncodeToString(h.Sum(nil)) != wantDigest {
			return fmt.Errorf("blobstore: content-addressed path contains different bytes: %s", rel)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("blobstore: inspect existing blob: %w", err)
	}
	return s.commitReplacement(tmpPath, rel)
}

func (s Store) commitReplacement(tmpPath, rel string) error {
	if err := s.prepareCommit(tmpPath, rel); err != nil {
		return err
	}
	stageRel, err := filepath.Rel(s.Root, tmpPath)
	if err != nil {
		return fmt.Errorf("blobstore: locate staged blob: %w", err)
	}
	if err := fseffect.Rename(s.Root, stageRel, filepath.FromSlash(rel)); err != nil {
		return fmt.Errorf("blobstore: commit blob: %w", err)
	}
	return nil
}

// PutDerived stores what the host derived from blob id under name, beside the blob. It is
// replaced whole on every write and removed with the blob.
func (s Store) PutDerived(id, name string, r io.Reader, limit bytebound.Materialization) error {
	if !s.Available() {
		return ErrNoStore
	}
	rel, err := s.derivedRel(id, name)
	if err != nil {
		return err
	}
	state, releaseState := s.acquireState()
	defer releaseState()
	state.uploadMu.Lock()
	defer state.uploadMu.Unlock()
	tmpPath, _, _, err := s.stage(state, r, limit, path.Base(rel))
	if err != nil {
		return err
	}
	defer s.releaseStage(state, tmpPath)
	state.mu.Lock()
	defer state.mu.Unlock()
	return s.commitReplacement(tmpPath, rel)
}

// OpenDerived reads a derived file's plaintext bytes; ErrNotFound when nothing was derived.
func (s Store) OpenDerived(id, name string) (io.ReadCloser, error) {
	if !s.Available() {
		return nil, ErrNoStore
	}
	rel, err := s.derivedRel(id, name)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(s.Root, filepath.FromSlash(rel)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s has no derived %s", ErrNotFound, id, name)
	}
	if err != nil {
		return nil, err
	}
	dec, err := zstd.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("blobstore: open derived decoder: %w", err)
	}
	return &decompressingBlob{file: f, dec: dec}, nil
}

// derivedRel is the host-data path of a derived file, once the blob it belongs to exists.
func (s Store) derivedRel(id, name string) (string, error) {
	if _, _, err := s.findEntry(id); err != nil {
		return "", err
	}
	leaf := SafeName(name)
	if leaf == "" || leaf != strings.TrimSpace(name) {
		return "", fmt.Errorf("blobstore: %q is not a derived file name", name)
	}
	return path.Join(strings.Trim(s.Dir, "/"), id, derivedDir, leaf), nil
}

// ErrNotFound means no blob is stored under the given id.
var ErrNotFound = errors.New("blobstore: no such blob")

// Resolve looks up a blob by ID.
func (s Store) Resolve(id string) (Blob, error) {
	if !s.Available() {
		return Blob{}, ErrNoStore
	}
	rel, name, err := s.findEntry(id)
	if err != nil {
		return Blob{}, err
	}
	// The on-disk file holds a compressed body; Blob.Size is the plaintext
	// contract every caller budgets against, so it is measured, not stat'd.
	size, err := plaintextSize(filepath.Join(s.Root, filepath.FromSlash(rel)))
	if err != nil {
		return Blob{}, fmt.Errorf("blobstore: measure blob %s: %w", id, err)
	}
	return Blob{ID: id, Rel: rel, Name: name, Size: size}, nil
}

// findEntry locates a blob's stored file without reading its body.
func (s Store) findEntry(id string) (rel, name string, err error) {
	if !isHexDigest(id) {
		return "", "", fmt.Errorf("%w: %q is not an attachment id", ErrNotFound, id)
	}
	dir := filepath.Join(s.dataRoot(), id)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return "", "", fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		return filepath.ToSlash(filepath.Join(strings.Trim(s.Dir, "/"), id, e.Name())), e.Name(), nil
	}
	return "", "", fmt.Errorf("%w: %s", ErrNotFound, id)
}

// plaintextSize streams and discards a compressed blob to measure its
// decoded length without materializing the body in memory.
func plaintextSize(abs string) (int64, error) {
	f, err := os.Open(abs)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	dec, err := zstd.NewReader(f)
	if err != nil {
		return 0, err
	}
	defer dec.Close()
	return io.Copy(io.Discard, dec)
}

// Retain records new retention claims for one prompt operation.
func (s Store) Retain(operationID string, ids []string) ([]string, error) {
	if !s.Available() {
		return nil, ErrNoStore
	}
	retentionID, err := retentionClaimID(operationID)
	if err != nil {
		return nil, err
	}
	state, releaseState := s.acquireState()
	defer releaseState()
	state.mu.Lock()
	defer state.mu.Unlock()
	normalizedIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if !isHexDigest(id) {
			return nil, fmt.Errorf("%w: %q is not an attachment id", ErrNotFound, id)
		}
		dir := filepath.Join(s.dataRoot(), id)
		if _, err := os.Stat(dir); err != nil {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		normalizedIDs = append(normalizedIDs, id)
	}
	acquired := make([]string, 0, len(normalizedIDs))
	for _, id := range normalizedIDs {
		marker := filepath.Join(s.dataRoot(), retentionDir, id, retentionID)
		if _, err := os.Stat(marker); err == nil {
			if err := touchRetentionMarker(s.dataRoot(), id, retentionID); err != nil {
				return nil, err
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err := touchRetentionMarker(s.dataRoot(), id, retentionID); err != nil {
			for _, acquiredID := range acquired {
				_ = s.releaseRetentionMarker(retentionID, acquiredID)
			}
			return nil, err
		}
		acquired = append(acquired, id)
	}
	return acquired, nil
}

func (s Store) releaseRetentionMarker(retentionID, id string) error {
	dir := filepath.Join(s.dataRoot(), retentionDir, id)
	err := os.Remove(filepath.Join(dir, retentionID))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("blobstore: remove retention marker: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) == 0 {
		_ = os.Remove(dir)
	}
	return nil
}

// Release removes one prompt operation's retention claims.
func (s Store) Release(operationID string, ids []string) error {
	if !s.Available() {
		return ErrNoStore
	}
	retentionID, err := retentionClaimID(operationID)
	if err != nil {
		return err
	}
	state, releaseState := s.acquireState()
	defer releaseState()
	state.mu.Lock()
	defer state.mu.Unlock()
	normalizedIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if !isHexDigest(id) {
			return fmt.Errorf("%w: %q is not an attachment id", ErrNotFound, id)
		}
		normalizedIDs = append(normalizedIDs, id)
	}
	var releaseErrs []error
	for _, id := range normalizedIDs {
		if err := s.releaseRetentionMarker(retentionID, id); err != nil {
			releaseErrs = append(releaseErrs, err)
		}
	}
	return errors.Join(releaseErrs...)
}

func retentionClaimID(operationID string) (string, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return "", fmt.Errorf("blobstore: retention operation id is required")
	}
	sum := sha256.Sum256([]byte(operationID))
	return hex.EncodeToString(sum[:]), nil
}

// DiscardStagedBefore removes an unretained blob older than cutoff.
func (s Store) DiscardStagedBefore(id string, cutoff time.Time) (bool, error) {
	if !s.Available() {
		return false, ErrNoStore
	}
	id = strings.TrimSpace(id)
	if !isHexDigest(id) {
		return false, fmt.Errorf("%w: %q is not an attachment id", ErrNotFound, id)
	}
	state, releaseState := s.acquireState()
	defer releaseState()
	state.mu.Lock()
	defer state.mu.Unlock()
	dir := filepath.Join(s.dataRoot(), id)
	if !cutoff.IsZero() {
		info, err := os.Stat(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		if err == nil && !info.ModTime().Before(cutoff) {
			return false, nil
		}
	}
	retained, err := s.hasRetentions(id)
	if err != nil {
		return false, err
	}
	if retained {
		return false, nil
	}
	return s.removeBlob(id, dir)
}

// Maintain advances bounded staging collection.
func (s Store) Maintain() error {
	if !s.Available() {
		return ErrNoStore
	}
	state, releaseState := s.acquireState()
	defer releaseState()
	state.mu.Lock()
	defer state.mu.Unlock()
	return s.pruneExpiredLocked(state, time.Now())
}

func (s Store) removeBlob(id, dir string) (bool, error) {
	if s.MaintenanceLease != nil {
		release, acquired := s.MaintenanceLease()
		if !acquired {
			return false, nil
		}
		defer release()
	}
	if s.Released != nil {
		if err := s.Released(id); err != nil {
			return false, err
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return false, fmt.Errorf("blobstore: remove staged blob %s: %w", id, err)
	}
	return true, nil
}

func (s Store) pruneExpiredLocked(state *storeState, now time.Time) error {
	root := s.dataRoot()
	if err := s.pruneStagingLocked(state, now); err != nil {
		return err
	}
	if s.StagedTTL <= 0 {
		state.contentPrune.close()
		return nil
	}
	entries, err := state.contentPrune.next(root)
	if err != nil {
		return fmt.Errorf("blobstore: list staged blobs: %w", err)
	}
	cutoff := now.Add(-s.StagedTTL)
	for _, entry := range entries {
		if !entry.IsDir() || !isHexDigest(entry.Name()) {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		retained, err := s.hasRetentions(entry.Name())
		if err != nil {
			return err
		}
		if retained {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if _, err := s.removeBlob(entry.Name(), dir); err != nil {
			return err
		}
	}
	return nil
}

func (c *directoryCursor) next(root string) ([]os.DirEntry, error) {
	if c.dir == nil {
		dir, err := os.Open(root)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		c.dir = dir
	}
	entries, err := c.dir.ReadDir(expiredPruneBatch)
	if errors.Is(err, io.EOF) || len(entries) < expiredPruneBatch {
		c.close()
		return entries, nil
	}
	if err != nil {
		c.close()
		return nil, err
	}
	return entries, nil
}

func (c *directoryCursor) close() {
	if c.dir != nil {
		_ = c.dir.Close()
		c.dir = nil
	}
}

func (s Store) pruneStagingLocked(state *storeState, now time.Time) error {
	ttl := s.StagedTTL
	if ttl <= 0 {
		ttl = defaultStagingTTL
	}
	cutoff := now.Add(-ttl)
	root := s.dataRoot()
	stageRoot := filepath.Join(root, stagingDir)
	staged, stageErr := state.stagingPrune.next(stageRoot)
	if stageErr != nil {
		return fmt.Errorf("blobstore: list staging files: %w", stageErr)
	}
	for _, entry := range staged {
		path := filepath.Join(stageRoot, entry.Name())
		if entry.IsDir() || state.stageIsActive(path) {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("blobstore: prune staging file: %w", removeErr)
		}
	}
	return nil
}

func (s Store) hasRetentions(id string) (bool, error) {
	if s.Retained != nil {
		retained, err := s.Retained(id)
		if err != nil || retained {
			return retained, err
		}
	}
	markerDir := filepath.Join(s.dataRoot(), retentionDir, id)
	entries, err := os.ReadDir(markerDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("blobstore: inspect retention marker: %w", err)
	}
	if s.Retained == nil {
		return len(entries) != 0, nil
	}
	cutoff := time.Now().Add(-retentionClaimGrace)
	retained := false
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil || !info.ModTime().Before(cutoff) {
			retained = true
			continue
		}
		if err := os.Remove(filepath.Join(markerDir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	if !retained {
		_ = os.Remove(markerDir)
	}
	return retained, nil
}

// Open returns a reader over a resolved blob's plaintext bytes, transparently
// decompressing the on-disk zstd body as it is read.
func (s Store) Open(b Blob) (io.ReadCloser, error) {
	if !s.Available() {
		return nil, ErrNoStore
	}
	rel, _, err := s.findEntry(b.ID)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(s.Root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	dec, err := zstd.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("blobstore: open blob decoder: %w", err)
	}
	return &decompressingBlob{file: f, dec: dec}, nil
}

// decompressingBlob streams a zstd decoder over its backing file, closing
// both together.
type decompressingBlob struct {
	file *os.File
	dec  *zstd.Decoder
}

func (d *decompressingBlob) Read(p []byte) (int, error) {
	return d.dec.Read(p)
}

func (d *decompressingBlob) Close() error {
	d.dec.Close()
	return d.file.Close()
}

// Recompress re-encodes b.Rel at best-compression zstd, replacing only if smaller.
// Returns false if already optimal or larger.
func (s Store) Recompress(b Blob) (bool, error) {
	if strings.TrimSpace(s.Root) == "" {
		return false, ErrNoStore
	}
	rel := filepath.ToSlash(strings.TrimSpace(b.Rel))
	if rel == "" || sandbox.HasParentTraversal(rel) {
		return false, fmt.Errorf("blobstore: %q is not a usable relative path", rel)
	}
	abs := filepath.Join(s.Root, filepath.FromSlash(rel))
	state, releaseState := s.acquireState()
	defer releaseState()
	state.mu.Lock()
	defer state.mu.Unlock()

	current, err := os.ReadFile(abs)
	if err != nil {
		return false, fmt.Errorf("blobstore: read blob for recompression: %w", err)
	}
	plain, err := zstdcodec.Decompress(current)
	if err != nil {
		return false, fmt.Errorf("blobstore: decode blob: %w", err)
	}
	recompressed, err := zstdcodec.CompressLevel(bytes.NewReader(plain), zstd.SpeedBestCompression)
	if err != nil {
		return false, fmt.Errorf("blobstore: recompress blob: %w", err)
	}
	if len(recompressed) >= len(current) {
		return false, nil
	}
	if err := s.replaceBlobBytes(state, rel, recompressed); err != nil {
		return false, err
	}
	return true, nil
}

// replaceBlobBytes stages already-encoded bytes and atomically commits them
// over rel, bypassing the plaintext-in/compressed-out path stage() applies.
func (s Store) replaceBlobBytes(state *storeState, rel string, encoded []byte) error {
	stageDir := filepath.Join(s.dataRoot(), stagingDir)
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return fmt.Errorf("blobstore: create staging dir: %w", err)
	}
	tmp, err := os.CreateTemp(stageDir, ".blob-*")
	if err != nil {
		return fmt.Errorf("blobstore: create staging file: %w", err)
	}
	tmpPath := tmp.Name()
	state.activeMu.Lock()
	state.active[tmpPath] = struct{}{}
	state.activeMu.Unlock()
	defer s.releaseStage(state, tmpPath)
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("blobstore: write recompressed blob: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("blobstore: chmod staging file: %w", err)
	}
	if err := fssync.File(tmp); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("blobstore: sync staging file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("blobstore: close staging file: %w", err)
	}
	return s.commitReplacement(tmpPath, rel)
}

func isHexDigest(id string) bool {
	if len(id) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// SafeName reduces a client-supplied filename to a single path-free leaf.
func SafeName(name string) string {
	leaf := strings.TrimSpace(name)
	leaf = strings.ReplaceAll(leaf, "\\", "/")
	if i := strings.LastIndex(leaf, "/"); i >= 0 {
		leaf = leaf[i+1:]
	}
	leaf = strings.TrimSpace(leaf)
	switch leaf {
	case "", ".", "..":
		return "blob"
	}
	return leaf
}
