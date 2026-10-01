// Package sourceblob stores content-addressed source bytes.
package sourceblob

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // SHA-1 identifies source objects.
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/contextio"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/fssync"
	"github.com/lycaon/lycaon/internal/hostlock"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

// ErrFileChanged rejects a capture modified during its read.
var ErrFileChanged = errors.New("source file changed while being captured")

const (
	// MaxRevisionContentBytes bounds content retained for one version.
	MaxRevisionContentBytes = 4 * 1024 * 1024

	shaHexLen         = sha256.Size * 2
	orphanGracePeriod = time.Hour
	orphanShardCount  = 256
	orphanBatchSize   = 128
)

// stagingDirName holds bodies that cannot be named until their last byte is read.
const stagingDirName = ".staging"

// Store is the content-addressed object root.
type Store struct {
	root string
	// mu orders object removal against readers and writers of one object.
	mu sync.RWMutex
	// Captures hold lifecycle through reference commit; maintenance only tries the lock.
	lifecycle *sync.RWMutex
	// guard, when set, is verified before maintenance deletes objects.
	guard hostlock.Guard
	// pruneMu serializes orphan passes, which share one cursor position.
	pruneMu       sync.Mutex
	orphanShard   uint16
	stagingCursor directoryCursor
	objectCursor  directoryCursor
}

type directoryCursor struct {
	dir *os.File
	rel string
}

var sourceLifecycles [64]sync.RWMutex

func sourceLifecycle(root string) *sync.RWMutex {
	var shard uint64
	for _, c := range []byte(root) {
		shard = shard*33 + uint64(c)
	}
	return &sourceLifecycles[shard%uint64(len(sourceLifecycles))]
}

func New(dir string) *Store {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" || dir == "." {
		return nil
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	dir = fspath.CanonicalPath(dir)
	return &Store{root: dir, lifecycle: sourceLifecycle(dir)}
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.pruneMu.Lock()
	defer s.pruneMu.Unlock()
	s.stagingCursor.close()
	s.objectCursor.close()
	return nil
}

func (s *Store) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// AcquireReferenceLease lets captures overlap through reference commit.
func (s *Store) AcquireReferenceLease() func() {
	if s == nil {
		return func() {}
	}
	s.lifecycle.RLock()
	return s.lifecycle.RUnlock
}

// SetGuard binds maintenance to the engine's store claim.
func (s *Store) SetGuard(guard hostlock.Guard) {
	if s != nil {
		s.guard = guard
	}
}

// TryAcquireMaintenanceLease avoids queuing a writer that would block new captures.
// ok is false while a capture holds the lifecycle; err means the guard failed and
// no lease is held.
func (s *Store) TryAcquireMaintenanceLease() (release func(), ok bool, err error) {
	if s == nil {
		return func() {}, true, nil
	}
	if !s.lifecycle.TryLock() {
		return nil, false, nil
	}
	if s.guard != nil {
		if err := s.guard.Verify(); err != nil {
			s.lifecycle.Unlock()
			return nil, false, err
		}
	}
	return s.lifecycle.Unlock, true, nil
}

func ContentSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Object IDs hash "blob <len>\x00" followed by content.
type GitOIDs struct {
	SHA1   string
	SHA256 string
}

func ContentGitOIDs(b []byte) GitOIDs {
	h1, h256 := newGitBlobHashers(int64(len(b)))
	h1.Write(b)
	h256.Write(b)
	return GitOIDs{
		SHA1:   hex.EncodeToString(h1.Sum(nil)),
		SHA256: hex.EncodeToString(h256.Sum(nil)),
	}
}

// The streaming hash includes the object header before content.
func GitBlobSHA1(size int64) hash.Hash {
	h1 := sha1.New() //nolint:gosec // SHA-1 identifies source objects.
	h1.Write(gitBlobHeader(size))
	return h1
}

func gitBlobHeader(size int64) []byte {
	return []byte(fmt.Sprintf("blob %d\x00", size))
}

func newGitBlobHashers(size int64) (hash.Hash, hash.Hash) {
	h256 := sha256.New()
	h256.Write(gitBlobHeader(size))
	return GitBlobSHA1(size), h256
}

func RelPath(sha string) (string, error) {
	sha = strings.ToLower(strings.TrimSpace(sha))
	if len(sha) != shaHexLen {
		return "", fmt.Errorf("invalid content sha")
	}
	if _, err := hex.DecodeString(sha); err != nil {
		return "", fmt.Errorf("invalid content sha: %w", err)
	}
	return filepath.Join(sha[:2], sha[2:]+".zst"), nil
}

// Capture identifies bytes and their observed file metadata.
type Capture struct {
	SHA256     string
	GitOIDs    GitOIDs
	Rel        string
	Size       int64
	Stored     int64
	Mode       uint32
	ModifiedNS int64
}

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
	if info, statErr := os.Stat(filepath.Join(s.root, rel)); statErr == nil {
		return rel, info.Size(), oids, nil
	}
	compressed, err := zstdcodec.Compress(bytes.NewReader(plain))
	if err != nil {
		return "", 0, GitOIDs{}, err
	}
	if err := s.commitBytes(rel, compressed); err != nil {
		return "", 0, GitOIDs{}, err
	}
	return rel, int64(len(compressed)), oids, nil
}

// PutFile hashes and compresses a worktree file in one read.
func (s *Store) PutFile(ctx context.Context, abs string) (Capture, error) {
	return s.putFile(ctx, abs, os.Lstat, os.Open, nil)
}

// CopyRootFile captures through a held root and optionally copies the same bytes.
// The destination remains private until capture and its own sync succeed.
func (s *Store) CopyRootFile(ctx context.Context, root *os.Root, path string, destination io.Writer) (Capture, error) {
	return s.putFile(ctx, path, root.Lstat, root.Open, destination)
}

func (s *Store) putFile(ctx context.Context, abs string, stat func(string) (os.FileInfo, error), open func(string) (*os.File, error), destination io.Writer) (Capture, error) {
	if err := ctx.Err(); err != nil {
		return Capture{}, err
	}
	if s == nil {
		return Capture{}, fmt.Errorf("content store unavailable")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	before, err := stat(abs)
	if err != nil {
		return Capture{}, err
	}
	if !before.Mode().IsRegular() {
		return Capture{}, fmt.Errorf("snapshot capture requires a regular file: %s", abs)
	}
	staged, err := s.stageFile(ctx, abs, before, open, destination)
	if staged.tempPath != "" {
		defer func() { _ = os.Remove(staged.tempPath) }()
	}
	if err != nil {
		return Capture{}, err
	}

	after, err := stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return Capture{}, ErrFileChanged
		}
		return Capture{}, err
	}
	if !sameFileVersion(before, staged.afterOpen) || !sameFileVersion(staged.afterOpen, after) {
		return Capture{}, ErrFileChanged
	}

	out := Capture{
		SHA256: staged.sha, GitOIDs: staged.gitOIDs, Size: after.Size(),
		Mode: uint32(after.Mode()), ModifiedNS: after.ModTime().UnixNano(),
	}
	out.Rel, err = RelPath(staged.sha)
	if err != nil {
		return Capture{}, err
	}
	matched, err := s.matchesCapture(ctx, out.Rel, staged)
	if err != nil {
		return Capture{}, err
	}
	if matched {
		out.Stored = staged.stored
		return out, nil
	}
	if err := s.promote(staged.tempPath, out.Rel); err != nil {
		return Capture{}, err
	}
	out.Stored = staged.stored
	return out, nil
}

// stagedFile holds a captured body before promotion.
type stagedFile struct {
	tempPath  string
	sha       string
	gitOIDs   GitOIDs
	stored    int64
	storedSHA string
	afterOpen os.FileInfo
}

func (s *Store) stageFile(ctx context.Context, abs string, before os.FileInfo, open func(string) (*os.File, error), destination io.Writer) (stagedFile, error) {
	expectedSize := before.Size()
	staging := filepath.Join(s.root, stagingDirName)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return stagedFile{}, err
	}
	temp, err := os.CreateTemp(staging, ".capture-*")
	if err != nil {
		return stagedFile{}, err
	}
	out := stagedFile{tempPath: temp.Name()}
	source, err := open(abs)
	if err != nil {
		_ = temp.Close()
		return out, err
	}
	opened, err := source.Stat()
	if err != nil || !sameFileVersion(before, opened) {
		_ = source.Close()
		_ = temp.Close()
		if err != nil {
			return out, err
		}
		return out, ErrFileChanged
	}
	digest := sha256.New()
	// Object headers include the pre-read size; copied-length checks reject size changes.
	gitSHA1, gitSHA256 := newGitBlobHashers(expectedSize)
	storedDigest := sha256.New()
	encoder, encErr := acquireEncoder(io.MultiWriter(temp, storedDigest))
	var copied int64
	var copyErr, closeEncErr error
	if encErr == nil {
		writers := []io.Writer{encoder, digest, gitSHA1, gitSHA256}
		if destination != nil {
			writers = append(writers, destination)
		}
		copied, copyErr = io.Copy(io.MultiWriter(writers...), contextio.Reader{Context: ctx, Source: source})
		closeEncErr = encoder.Close()
		releaseEncoder(encoder)
	}
	afterOpen, statErr := source.Stat()
	closeSourceErr := source.Close()
	size, sizeErr := temp.Seek(0, io.SeekEnd)
	closeTempErr := temp.Close()
	if err := errors.Join(encErr, copyErr, closeEncErr, statErr,
		closeSourceErr, sizeErr, closeTempErr); err != nil {
		return out, err
	}
	if copied != expectedSize {
		return out, ErrFileChanged
	}
	out.sha = hex.EncodeToString(digest.Sum(nil))
	out.gitOIDs = GitOIDs{
		SHA1:   hex.EncodeToString(gitSHA1.Sum(nil)),
		SHA256: hex.EncodeToString(gitSHA256.Sum(nil)),
	}
	out.stored = size
	out.storedSHA = hex.EncodeToString(storedDigest.Sum(nil))
	out.afterOpen = afterOpen
	return out, nil
}

// promote makes a staged body durable under its digest.
func (s *Store) promote(tempPath, rel string) error {
	temp, err := os.OpenFile(tempPath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	syncErr := fssync.File(temp)
	closeErr := temp.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return err
	}
	if err := os.Chmod(tempPath, 0o400); err != nil {
		return err
	}
	dest := filepath.Join(s.root, rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	restoreMode, err := prepareObjectPublication(dest)
	if err != nil {
		return err
	}
	defer restoreMode()
	// Replacement repairs damaged objects under the same digest.
	if err := os.Rename(tempPath, dest); err != nil {
		return err
	}
	return syncDir(filepath.Dir(dest))
}

// commitBytes atomically publishes a complete object.
func (s *Store) commitBytes(rel string, compressed []byte) error {
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return fmt.Errorf("create content object directory: %w", err)
	}
	if _, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: s.root, Rel: rel},
		Source:   bytes.NewReader(compressed),
		Mode:     0o600,
		DirMode:  0o700,
	}); err != nil {
		return fmt.Errorf("commit content object: %w", err)
	}
	return nil
}

func (s *Store) Get(rel string) ([]byte, error) {
	if s == nil {
		return nil, os.ErrNotExist
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	safe, err := s.resolve(rel)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(safe)
	if err != nil {
		return nil, err
	}
	return zstdcodec.Decompress(raw)
}

func (s *Store) GetSHA(sha string) ([]byte, error) {
	rel, err := RelPath(sha)
	if err != nil {
		return nil, err
	}
	return s.Get(rel)
}

func (s *Store) Exists(rel string) (bool, error) {
	if s == nil {
		return false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	safe, err := s.resolve(rel)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(safe)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

// Remove deletes one object, tolerating an already-absent file.
func (s *Store) Remove(rel string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	safe, err := s.resolve(rel)
	if err != nil {
		return err
	}
	err = os.Remove(safe)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *Store) resolve(rel string) (string, error) {
	rel = filepath.Clean(strings.TrimSpace(rel))
	if rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid content object path")
	}
	return filepath.Join(s.root, rel), nil
}

// OrphanPass reports what one bounded orphan pass covered.
type OrphanPass struct {
	// Checked counts objects past the grace period.
	Checked int
	// Removed counts unreferenced objects removed or already absent.
	Removed int
	// CycleComplete marks the end of a traversal through all content shards.
	CycleComplete bool
}

// Prune removes unreferenced objects in bounded batches after the grace period.
// Reference checks run without the store lock and may query a database.
func (s *Store) PruneOrphansBatch(isReferenced func(rel string) (bool, error)) (OrphanPass, error) {
	if s == nil {
		return OrphanPass{}, nil
	}
	s.pruneMu.Lock()
	defer s.pruneMu.Unlock()
	contentRoot, err := os.OpenRoot(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return OrphanPass{CycleComplete: true}, nil
	}
	if err != nil {
		return OrphanPass{}, err
	}
	defer func() { _ = contentRoot.Close() }()
	cutoff := time.Now().Add(-orphanGracePeriod)
	var pass OrphanPass
	staging, err := s.stagingCursor.next(s.root, stagingDirName)
	if err != nil {
		return pass, err
	}
	if err := s.pruneOrphanEntries(&pass, contentRoot, stagingDirName, staging, cutoff, isReferenced); err != nil {
		return pass, err
	}
	shard, entries, err := s.nextOrphanObjectBatch()
	if err != nil {
		return pass, err
	}
	if err := s.pruneOrphanEntries(&pass, contentRoot, shard, entries, cutoff, isReferenced); err != nil {
		return pass, err
	}
	pass.CycleComplete = s.objectCursor.dir == nil && shard == lastOrphanShard
	return pass, nil
}

var lastOrphanShard = fmt.Sprintf("%02x", orphanShardCount-1)

func (s *Store) nextOrphanObjectBatch() (string, []os.DirEntry, error) {
	if s.objectCursor.dir == nil {
		s.objectCursor.rel = fmt.Sprintf("%02x", s.orphanShard)
		s.orphanShard = (s.orphanShard + 1) % orphanShardCount
	}
	dir := s.objectCursor.rel
	entries, err := s.objectCursor.next(s.root, dir)
	return dir, entries, err
}

// Reference queries run outside mu; removal waits for object readers and writers.
func (s *Store) pruneOrphanEntries(
	pass *OrphanPass,
	contentRoot *os.Root,
	dir string,
	entries []os.DirEntry,
	cutoff time.Time,
	isReferenced func(string) (bool, error),
) error {
	orphans := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		pass.Checked++
		rel := filepath.Join(dir, entry.Name())
		referenced, err := isReferenced(rel)
		if err != nil {
			return err
		}
		if !referenced {
			orphans = append(orphans, rel)
		}
	}
	if len(orphans) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rel := range orphans {
		if err := contentRoot.Remove(rel); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		pass.Removed++
	}
	return nil
}

func (c *directoryCursor) next(root, rel string) ([]os.DirEntry, error) {
	if c.dir == nil {
		dir, err := os.Open(filepath.Join(root, rel))
		if errors.Is(err, os.ErrNotExist) {
			c.rel = ""
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		c.dir = dir
		c.rel = rel
	}
	entries, err := c.dir.ReadDir(orphanBatchSize)
	if errors.Is(err, io.EOF) {
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
	}
	c.dir = nil
	c.rel = ""
}

func sameFileVersion(left, right os.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right) &&
		left.Size() == right.Size() && left.Mode() == right.Mode() &&
		left.ModTime().Equal(right.ModTime())
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return fssync.File(dir)
}
