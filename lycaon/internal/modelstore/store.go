// Package modelstore manages verified local model artifacts and their activation.
package modelstore

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// MaxArtifactBytes bounds a staged payload.
const MaxArtifactBytes = 64 << 30

// MaxJournalBytes bounds journal recovery size.
const MaxJournalBytes = 16 << 20

var digestShape = regexp.MustCompile(`^[0-9a-f]{64}$`)
var nameShape = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ReadinessState reports the lifecycle state of a model slot.
type ReadinessState string

const (
	StateAbsent         ReadinessState = "absent"
	StateStaging        ReadinessState = "staging"
	StateVerified       ReadinessState = "verified"
	StateReady          ReadinessState = "ready"
	StateRepairRequired ReadinessState = "repair_required"
)

// RepairAction identifies the recovery action needed when a model is not ready.
type RepairAction string

const (
	RepairNone            RepairAction = "none"
	RepairImportSeed      RepairAction = "import_seed"
	RepairRestageArtifact RepairAction = "restage_artifact"
	RepairRecoverJournal  RepairAction = "recover_journal"
)

// Readiness summarizes the state and available repair action for a model slot.
type Readiness struct {
	State    ReadinessState `json:"state"`
	Active   *Manifest      `json:"active,omitempty"`
	Previous *Manifest      `json:"previous,omitempty"`
	Repair   RepairAction   `json:"repair"`
	Reason   string         `json:"reason,omitempty"`
}

// SpaceRequirement breaks down the disk bytes needed before mutating the store.
type SpaceRequirement struct {
	StagingBytes   int64 `json:"staging_bytes"`
	UnpackingBytes int64 `json:"unpacking_bytes"`
	SeedDuplicate  int64 `json:"seed_duplicate_bytes"`
	RollbackBytes  int64 `json:"rollback_bytes"`
}

// RequiredBytes returns total bytes needed across staging, unpacking, seed duplication, and rollback retention.
func (r SpaceRequirement) RequiredBytes() (int64, error) {
	var total int64
	for _, n := range []int64{r.StagingBytes, r.UnpackingBytes, r.SeedDuplicate, r.RollbackBytes} {
		if n < 0 || n > math.MaxInt64-total {
			return 0, errors.New("modelstore: invalid disk-space budget")
		}
		total += n
	}
	return total, nil
}

// ErrInsufficientSpace indicates that available disk space cannot cover the required mutation budget.
var ErrInsufficientSpace = errors.New("modelstore: insufficient disk space")

// Artifact identifies one immutable model payload.
type Artifact struct {
	Name   string `json:"name,omitempty"`
	Digest string `json:"digest"`
	Format string `json:"format"`
	Bytes  int64  `json:"bytes"`
}

// Manifest describes one model revision and its backing payload.
type Manifest struct {
	Model    string     `json:"model"`
	Revision string     `json:"revision"`
	Artifact Artifact   `json:"artifact"`
	Files    []Artifact `json:"files,omitempty"`
	Device   string     `json:"device"`
}

// SignedManifest pairs a manifest with its detached authority signature.
type SignedManifest struct {
	Manifest  Manifest `json:"manifest"`
	Signature string   `json:"signature"`
	KeyID     string   `json:"key_id"`
}

// Signer verifies manifest signatures against host-trusted keys.
type Signer interface {
	Verify(SignedManifest) error
}

// Lease pins an artifact revision in use by a runtime process so collection cannot remove it.
type Lease struct {
	lock       *os.File
	Manifest   Manifest
	Owner      string
	Generation uint64
	Dir        string
	once       sync.Once
}

// CheckpointDir returns the materialized directory for the leased revision.
func (l *Lease) CheckpointDir() string {
	if l == nil {
		return ""
	}
	return l.Dir
}

// Release drops the lease on the pinned revision.
func (l *Lease) Release() {
	if l == nil || l.lock == nil {
		return
	}
	l.once.Do(func() {
		_ = l.lock.Close()
	})
}

// Store manages content-addressed artifacts, the activation journal, and active leases.
type Store struct {
	root      string
	signer    Signer
	freeBytes func(string) (int64, error)

	mu sync.Mutex
}

// DefaultRoot returns the host model store directory.
func DefaultRoot() string {
	if dir := os.Getenv("LYCAON_MODEL_STORE_DIR"); dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "share", "lycaon", "models")
	}
	return filepath.Join(os.TempDir(), "lycaon-models")
}

func New(root string, signer Signer) *Store {
	return &Store{
		root:      root,
		signer:    signer,
		freeBytes: defaultFreeBytes,
	}
}

// Root returns the filesystem root of the store.
func (s *Store) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// SetFreeBytesFunc overrides disk-space probing for deterministic testing.
func (s *Store) SetFreeBytesFunc(fn func(string) (int64, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.freeBytes = fn
}

func (s *Store) artifactRel(digest string) string {
	return filepath.Join("artifacts", digest[:2], digest)
}

// openRoot opens the store directory via os.Root so symlinked components inside the store cannot escape it.
func (s *Store) openRoot() (*os.Root, error) {
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return nil, fmt.Errorf("modelstore: root: %w", err)
	}
	return os.OpenRoot(s.root)
}

// CheckSpace verifies that the store root has enough free bytes for req.
func (s *Store) CheckSpace(req SpaceRequirement) error {
	need, err := req.RequiredBytes()
	if err != nil {
		return err
	}
	if need <= 0 {
		return nil
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return fmt.Errorf("modelstore: root: %w", err)
	}
	s.mu.Lock()
	probe := s.freeBytes
	s.mu.Unlock()
	if probe == nil {
		return nil
	}
	avail, err := probe(s.root)
	if err != nil {
		return fmt.Errorf("modelstore: statfs: %w", err)
	}
	if avail < need {
		return fmt.Errorf("%w: need %d bytes, available %d bytes", ErrInsufficientSpace, need, avail)
	}
	return nil
}
