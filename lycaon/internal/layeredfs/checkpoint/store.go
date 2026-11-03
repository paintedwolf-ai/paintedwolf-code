package checkpoint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/fssync"
)

// RetentionRecord tracks the immutable blobs and private Git objects pinned by a checkpoint.
type RetentionRecord struct {
	CheckpointID string    `json:"checkpoint_id"`
	Blobs        []string  `json:"blobs"`
	GitObjects   []string  `json:"git_objects,omitempty"`
	CommittedAt  time.Time `json:"committed_at"`
}

// Store manages the durable content-addressed checkpoint store on disk.
type Store struct {
	rootDir        string
	blobsDir       string
	checkpointsDir string
	retentionDir   string
	promotionsDir  string
	mu             sync.RWMutex
	tempSeq        atomic.Uint64
}

// NewStore initializes or opens a durable checkpoint store at rootDir.
func NewStore(rootDir string) (*Store, error) {
	if strings.TrimSpace(rootDir) == "" {
		return nil, fmt.Errorf("checkpoint: rootDir is required")
	}
	s := &Store{
		rootDir:        rootDir,
		blobsDir:       filepath.Join(rootDir, "blobs"),
		checkpointsDir: filepath.Join(rootDir, "checkpoints"),
		retentionDir:   filepath.Join(rootDir, "retention"),
		promotionsDir:  filepath.Join(rootDir, "promotions"),
	}
	for _, d := range []string{s.rootDir, s.blobsDir, s.checkpointsDir, s.retentionDir, s.promotionsDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("checkpoint: mkdir %s: %w", d, err)
		}
	}
	return s, nil
}

// RootDir returns the store's root directory path.
func (s *Store) RootDir() string {
	return s.rootDir
}

// BlobPath returns the content-addressed path for a blob digest.
func (s *Store) BlobPath(sha string) string {
	return filepath.Join(s.blobsDir, sha)
}

// WriteBlobBytes writes an immutable read-only blob from memory and returns its SHA-256 and path.
func (s *Store) WriteBlobBytes(data []byte) (string, string, error) {
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	_, err := s.WriteBlob(bytes.NewReader(data), sha, int64(len(data)))
	if err != nil {
		return "", "", err
	}
	return sha, s.BlobPath(sha), nil
}

// WriteBlob copies r into an independent immutable 0400 blob file and validates its digest and size.
func (s *Store) WriteBlob(r io.Reader, expectedSHA string, expectedSize int64) (string, error) {
	seq := s.tempSeq.Add(1)
	tmpPath := filepath.Join(s.blobsDir, fmt.Sprintf(".blob-%d.tmp", seq))
	f, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), r)
	chmodErr := f.Chmod(0o400)
	syncErr := syncFile(f)
	closeErr := f.Close()
	if err := errors.Join(copyErr, chmodErr, syncErr, closeErr); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	actualSHA := hex.EncodeToString(h.Sum(nil))
	if (expectedSHA != "" && actualSHA != expectedSHA) || (expectedSize >= 0 && n != expectedSize) {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("%w: got sha=%s size=%d, want sha=%s size=%d", ErrCorruptBlob, actualSHA, n, expectedSHA, expectedSize)
	}
	finalPath := s.BlobPath(actualSHA)
	if _, err := os.Stat(finalPath); err == nil {
		_ = os.Remove(tmpPath)
		if _, vErr := s.VerifyBlob(actualSHA, n); vErr != nil {
			return "", vErr
		}
		return actualSHA, nil
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := syncDir(s.blobsDir); err != nil {
		return "", err
	}
	return actualSHA, nil
}

// VerifyBlob checks that the immutable blob exists and matches the expected SHA-256 and size.
func (s *Store) VerifyBlob(sha string, expectedSize int64) (string, error) {
	if sha == "" {
		return "", ErrMissingBaseBlob
	}
	p := s.BlobPath(sha)
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: %s", ErrMissingBaseBlob, sha)
		}
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", err
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if actual != sha || (expectedSize >= 0 && n != expectedSize) {
		return "", fmt.Errorf("%w: blob %s got sha=%s size=%d, want size=%d", ErrCorruptBlob, sha, actual, n, expectedSize)
	}
	return p, nil
}

// ReadBlob reads and verifies the full contents of a content-addressed blob.
func (s *Store) ReadBlob(sha string) ([]byte, error) {
	if sha == "" {
		return nil, ErrMissingBaseBlob
	}
	raw, err := os.ReadFile(s.BlobPath(sha))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrMissingBaseBlob, sha)
		}
		return nil, err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != sha {
		return nil, fmt.Errorf("%w: %s", ErrCorruptBlob, sha)
	}
	return raw, nil
}

func (s *Store) stageManifest(m Manifest, beforeSync func() error) error {
	cpDir := filepath.Join(s.checkpointsDir, m.CheckpointID)
	if err := os.MkdirAll(cpDir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := filepath.Join(cpDir, "manifest.json.tmp")
	_ = os.Remove(tmpPath)
	f, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, wErr := f.Write(raw)
	cErr := f.Chmod(0o400)
	if wErr != nil || cErr != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return errors.Join(wErr, cErr)
	}
	if beforeSync != nil {
		if err := beforeSync(); err != nil {
			_ = f.Close()
			_ = os.Remove(tmpPath)
			return err
		}
	}
	sErr := syncFile(f)
	closeErr := f.Close()
	if err := errors.Join(sErr, closeErr); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func (s *Store) commitManifestAndLatest(checkpointID string) error {
	cpDir := filepath.Join(s.checkpointsDir, checkpointID)
	tmpPath := filepath.Join(cpDir, "manifest.json.tmp")
	finalPath := filepath.Join(cpDir, "manifest.json")
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return err
	}
	if err := syncDir(cpDir); err != nil {
		return err
	}
	if err := syncDir(s.checkpointsDir); err != nil {
		return err
	}
	return writeAtomicJSONOrText(s.rootDir, "latest", []byte(checkpointID+"\n"), 0o600)
}

func (s *Store) writeRetentionRecord(rec RetentionRecord) error {
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomicJSONOrText(s.retentionDir, rec.CheckpointID+".json", raw, 0o600)
}

func writeAtomicJSONOrText(dir, name string, raw []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmpPath := filepath.Join(dir, "."+name+".tmp")
	_ = os.Remove(tmpPath)
	f, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, wErr := f.Write(raw)
	mErr := f.Chmod(mode)
	sErr := syncFile(f)
	cErr := f.Close()
	if err := errors.Join(wErr, mErr, sErr, cErr); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return syncDir(dir)
}

func syncFile(f *os.File) error {
	if fssync.Relaxed() {
		return nil
	}
	return f.Sync()
}

func syncDir(dirPath string) error {
	if fssync.Relaxed() {
		return nil
	}
	df, err := os.Open(dirPath)
	if err != nil {
		return err
	}
	sErr := df.Sync()
	cErr := df.Close()
	return errors.Join(sErr, cErr)
}
