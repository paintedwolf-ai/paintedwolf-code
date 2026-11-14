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
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
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

func isSafeStoreID(id string) bool {
	clean := strings.TrimSpace(id)
	return clean != "" && clean == id && safeComponent(clean) && !strings.Contains(clean, ":")
}

func isHexSHA256(sha string) bool {
	if len(sha) != 64 {
		return false
	}
	for i := range len(sha) {
		c := sha[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// BlobPath returns the content-addressed path for a blob digest.
func (s *Store) BlobPath(sha string) string {
	if !isHexSHA256(sha) {
		return filepath.Join(s.blobsDir, "_invalid_blob_")
	}
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
	if expectedSHA != "" && !isHexSHA256(expectedSHA) {
		return "", fmt.Errorf("%w: invalid expected sha256 %q", ErrCorruptBlob, expectedSHA)
	}
	tmpName := fmt.Sprintf(".blob-%d.tmp", s.tempSeq.Add(1))
	tmpPath := filepath.Join(s.blobsDir, tmpName)
	res, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: s.blobsDir, Rel: tmpName},
		Source:   r,
		Mode:     0o400,
		DirMode:  0o700,
	})
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	actualSHA := res.SHA256
	n := res.Bytes
	if (expectedSHA != "" && actualSHA != expectedSHA) || (expectedSize >= 0 && n != expectedSize) {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("%w: got sha=%s size=%d, want sha=%s size=%d", ErrCorruptBlob, actualSHA, n, expectedSHA, expectedSize)
	}
	finalPath := s.BlobPath(actualSHA)
	if _, err := os.Stat(finalPath); err == nil {
		if _, vErr := s.VerifyBlob(actualSHA, n); vErr == nil {
			_ = os.Remove(tmpPath)
			return actualSHA, nil
		}
	}
	if err := fseffect.Rename(s.blobsDir, tmpName, actualSHA); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	return actualSHA, nil
}

// VerifyBlob checks that the immutable blob exists and matches the expected SHA-256 and size.
func (s *Store) VerifyBlob(sha string, expectedSize int64) (string, error) {
	if sha == "" {
		return "", ErrMissingBaseBlob
	}
	if !isHexSHA256(sha) {
		return "", fmt.Errorf("%w: invalid blob sha256 %q", ErrCorruptBlob, sha)
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
	if !isHexSHA256(sha) {
		return nil, fmt.Errorf("%w: invalid blob sha256 %q", ErrCorruptBlob, sha)
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
	if !isSafeStoreID(m.CheckpointID) {
		return fmt.Errorf("%w: invalid checkpoint id %q", ErrInvalidManifest, m.CheckpointID)
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: s.checkpointsDir, Rel: filepath.Join(m.CheckpointID, "manifest.json.tmp")},
		Source:   bytes.NewReader(raw),
		Mode:     0o400,
		DirMode:  0o700,
		BeforeCommit: func(fseffect.Target, fseffect.Result) error {
			if beforeSync != nil {
				return beforeSync()
			}
			return nil
		},
	})
	return err
}

func (s *Store) commitManifestAndLatest(checkpointID string) error {
	if !isSafeStoreID(checkpointID) {
		return fmt.Errorf("%w: invalid checkpoint id %q", ErrInvalidManifest, checkpointID)
	}
	cpDir := filepath.Join(s.checkpointsDir, checkpointID)
	if err := fseffect.Rename(cpDir, "manifest.json.tmp", "manifest.json"); err != nil {
		return err
	}
	if err := syncDir(s.checkpointsDir); err != nil {
		return err
	}
	return writeAtomicJSONOrText(s.rootDir, "latest", []byte(checkpointID+"\n"), 0o600)
}

func (s *Store) writeRetentionRecord(rec RetentionRecord) error {
	if !isSafeStoreID(rec.CheckpointID) {
		return fmt.Errorf("%w: invalid checkpoint id %q", ErrInvalidManifest, rec.CheckpointID)
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomicJSONOrText(s.retentionDir, rec.CheckpointID+".json", raw, 0o600)
}

func writeAtomicJSONOrText(dir, name string, raw []byte, mode fs.FileMode) error {
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: dir, Rel: name},
		Source:   bytes.NewReader(raw),
		Mode:     mode,
		DirMode:  0o750,
	})
	return err
}

func syncFile(f *os.File) error {
	return fssync.File(f)
}

func syncDir(dirPath string) error {
	if fssync.Relaxed() {
		return nil
	}
	df, err := os.Open(dirPath)
	if err != nil {
		return err
	}
	sErr := fssync.File(df)
	cErr := df.Close()
	return errors.Join(sErr, cErr)
}
