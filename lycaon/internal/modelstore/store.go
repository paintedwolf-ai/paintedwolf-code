// Package modelstore manages staging, verification, activation, leasing,
// and garbage collection of local model artifacts.
package modelstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/filelock"
	"github.com/lycaon/lycaon/internal/fssync"
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
	UnpackingBytes int64 `json:" unpacking_bytes"`
	SeedDuplicate  int64 `json:"seed_duplicate_bytes"`
	RollbackBytes  int64 `json:"rollback_bytes"`
}

// RequiredBytes returns total bytes needed across staging, unpacking, seed duplication, and rollback retention.
func (r SpaceRequirement) RequiredBytes() int64 {
	return r.StagingBytes + r.UnpackingBytes + r.SeedDuplicate + r.RollbackBytes
}

// ErrInsufficientSpace indicates that available disk space cannot cover the required mutation budget.
var ErrInsufficientSpace = errors.New("modelstore: insufficient disk space")

// Artifact identifies one immutable model payload.
type Artifact struct {
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
}

// Signer verifies manifest signatures against host-trusted keys.
type Signer interface {
	Verify(SignedManifest) error
}

// Lease pins an artifact revision in use by a runtime process so collection cannot remove it.
type Lease struct {
	store      *Store
	Manifest   Manifest
	Owner      string
	Generation uint64
	once       sync.Once
}

// Release drops the lease on the pinned revision.
func (l *Lease) Release() {
	if l == nil || l.store == nil {
		return
	}
	l.once.Do(func() {
		l.store.releaseLease(l.Manifest.Artifact.Digest)
	})
}

// Store manages content-addressed artifacts, the activation journal, and active leases.
type Store struct {
	root      string
	signer    Signer
	freeBytes func(string) (int64, error)

	mu     sync.Mutex
	leases map[string]int
}

func New(root string, signer Signer) *Store {
	return &Store{
		root:      root,
		signer:    signer,
		freeBytes: defaultFreeBytes,
		leases:    make(map[string]int),
	}
}

// SetFreeBytesFunc overrides disk-space probing for deterministic testing.
func (s *Store) SetFreeBytesFunc(fn func(string) (int64, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.freeBytes = fn
}

func defaultFreeBytes(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}

func validate(m Manifest) error {
	if !nameShape.MatchString(m.Model) {
		return fmt.Errorf("modelstore: model name %q is not a safe identity", m.Model)
	}
	if !nameShape.MatchString(m.Revision) {
		return fmt.Errorf("modelstore: revision %q is not a safe identity", m.Revision)
	}
	if !nameShape.MatchString(m.Device) {
		return fmt.Errorf("modelstore: device %q is not a safe identity", m.Device)
	}
	if err := validateArtifact(m.Artifact); err != nil {
		return err
	}
	seen := map[string]bool{m.Artifact.Digest: true}
	for _, f := range m.Files {
		if err := validateArtifact(f); err != nil {
			return err
		}
		if seen[f.Digest] {
			return fmt.Errorf("modelstore: duplicate artifact entry %s", f.Digest)
		}
		seen[f.Digest] = true
	}
	return nil
}

func validateArtifact(a Artifact) error {
	if !digestShape.MatchString(a.Digest) {
		return fmt.Errorf("modelstore: digest %q is not a hex sha256", a.Digest)
	}
	if a.Bytes <= 0 || a.Bytes > MaxArtifactBytes {
		return fmt.Errorf("modelstore: artifact size %d out of bounds", a.Bytes)
	}
	return nil
}

func (s *Store) artifactRel(digest string) string {
	return filepath.Join("artifacts", digest[:2], digest)
}

// openRoot opens the store directory via os.Root so symlinked components inside the store cannot escape it.
func (s *Store) openRoot() (*os.Root, error) {
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return nil, fmt.Errorf("modelstore: root: %w", err)
	}
	return os.OpenRoot(s.root)
}

func (s *Store) journalPath() string { return filepath.Join(s.root, "journal.jsonl") }

type journalLock struct{ f *os.File }

func (s *Store) lockJournal() (*journalLock, error) {
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return nil, fmt.Errorf("modelstore: root: %w", err)
	}
	f, err := filelock.Open(filepath.Join(s.root, "journal.lock"))
	if err != nil {
		return nil, fmt.Errorf("modelstore: lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("modelstore: lock: %w", err)
	}
	return &journalLock{f}, nil
}

func (l *journalLock) Unlock() {
	_ = filelock.Unlock(l.f)
	_ = l.f.Close()
}

// CheckSpace verifies that the store root has enough free bytes for req.
func (s *Store) CheckSpace(req SpaceRequirement) error {
	need := req.RequiredBytes()
	if need <= 0 {
		return nil
	}
	if err := os.MkdirAll(s.root, 0o755); err != nil {
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

// Stage copies a payload into the store under its digest after verifying size, hash, and disk space.
func (s *Store) Stage(r io.Reader, want Artifact) error {
	if err := validateArtifact(want); err != nil {
		return err
	}
	if err := s.CheckSpace(SpaceRequirement{StagingBytes: want.Bytes}); err != nil {
		return err
	}
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	dir := filepath.Join("artifacts", want.Digest[:2])
	if err := root.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("modelstore: mkdir: %w", err)
	}
	tmpName := filepath.Join(dir, ".staging-"+fmt.Sprint(time.Now().UnixNano()))
	tmp, err := root.Create(tmpName)
	if err != nil {
		return fmt.Errorf("modelstore: temp: %w", err)
	}
	defer root.Remove(tmpName)

	h := sha256.New()
	limited := io.LimitReader(r, want.Bytes+1)
	n, err := io.Copy(io.MultiWriter(tmp, h), limited)
	if err != nil {
		tmp.Close()
		return fmt.Errorf("modelstore: copy: %w", err)
	}
	if n != want.Bytes {
		tmp.Close()
		return fmt.Errorf("modelstore: size mismatch: got %d want %d", n, want.Bytes)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want.Digest {
		tmp.Close()
		return fmt.Errorf("modelstore: digest mismatch: got %s want %s", got, want.Digest)
	}
	if err := fssync.File(tmp); err != nil {
		tmp.Close()
		return fmt.Errorf("modelstore: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("modelstore: close: %w", err)
	}
	if err := root.Rename(tmpName, s.artifactRel(want.Digest)); err != nil {
		return fmt.Errorf("modelstore: publish: %w", err)
	}
	dfd, err := root.Open(dir)
	if err != nil {
		return fmt.Errorf("modelstore: publish dir: %w", err)
	}
	if err := fssync.File(dfd); err != nil {
		dfd.Close()
		return fmt.Errorf("modelstore: publish dir sync: %w", err)
	}
	if err := dfd.Close(); err != nil {
		return fmt.Errorf("modelstore: publish dir close: %w", err)
	}
	return nil
}

// Verified reports whether an artifact is present on disk with the expected sha256 digest.
func (s *Store) Verified(digest string) bool {
	if !digestShape.MatchString(digest) {
		return false
	}
	root, err := s.openRoot()
	if err != nil {
		return false
	}
	defer root.Close()
	f, err := root.Open(s.artifactRel(digest))
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == digest
}

// Activate verifies manifest authority and appends the manifest to the activation journal under lock.
func (s *Store) Activate(signed SignedManifest) error {
	if s.signer == nil {
		return errors.New("modelstore: no manifest authority configured")
	}
	if err := s.signer.Verify(signed); err != nil {
		return fmt.Errorf("modelstore: manifest authority: %w", err)
	}
	m := signed.Manifest
	if err := validate(m); err != nil {
		return err
	}
	if !s.Verified(m.Artifact.Digest) {
		return fmt.Errorf("modelstore: artifact %s is not verified", m.Artifact.Digest)
	}
	for _, f := range m.Files {
		if !s.Verified(f.Digest) {
			return fmt.Errorf("modelstore: supplementary artifact %s is not verified", f.Digest)
		}
	}
	lock, err := s.lockJournal()
	if err != nil {
		return err
	}
	defer lock.Unlock()

	if err := s.recoverJournal(); err != nil {
		return err
	}
	f, err := os.OpenFile(s.journalPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("modelstore: journal: %w", err)
	}
	defer f.Close()
	line, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("modelstore: append: %w", err)
	}
	if err := fssync.File(f); err != nil {
		return fmt.Errorf("modelstore: journal sync: %w", err)
	}
	if dfd, err := os.Open(s.root); err != nil {
		return fmt.Errorf("modelstore: journal dir: %w", err)
	} else if err := fssync.File(dfd); err != nil {
		dfd.Close()
		return fmt.Errorf("modelstore: journal dir sync: %w", err)
	} else if err := dfd.Close(); err != nil {
		return fmt.Errorf("modelstore: journal dir close: %w", err)
	}
	return nil
}

// recoverJournal validates all complete records and truncates any torn trailing line under the journal lock.
func (s *Store) recoverJournal() error {
	raw, err := os.ReadFile(s.journalPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("modelstore: read journal: %w", err)
	}
	if len(raw) > MaxJournalBytes {
		return fmt.Errorf("modelstore: journal of %d bytes exceeds the recovery bound", len(raw))
	}
	torn := len(raw) > 0 && raw[len(raw)-1] != '\n'
	lines := splitLines(raw)
	if torn && len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}
	for i, line := range lines {
		var m Manifest
		if err := json.Unmarshal(line, &m); err != nil {
			return fmt.Errorf("modelstore: journal record %d is corrupt: %w", i, err)
		}
		if err := validate(m); err != nil {
			return fmt.Errorf("modelstore: journal record %d is invalid: %w", i, err)
		}
	}
	if !torn {
		return nil
	}
	tail := 0
	if idx := lastNewline(raw); idx >= 0 {
		tail = idx + 1
	}
	if err := os.Truncate(s.journalPath(), int64(tail)); err != nil {
		return err
	}
	if f, err := os.Open(s.journalPath()); err == nil {
		_ = fssync.File(f)
		_ = f.Close()
	}
	return nil
}

func lastNewline(raw []byte) int {
	for i := len(raw) - 1; i >= 0; i-- {
		if raw[i] == '\n' {
			return i
		}
	}
	return -1
}

// readRecords parses all complete records from the journal, skipping an unstaged torn final line.
func (s *Store) readRecords() ([]Manifest, error) {
	raw, err := os.ReadFile(s.journalPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("modelstore: read journal: %w", err)
	}
	if len(raw) > MaxJournalBytes {
		return nil, fmt.Errorf("modelstore: journal of %d bytes exceeds the recovery bound", len(raw))
	}
	lines := splitLines(raw)
	torn := len(raw) > 0 && raw[len(raw)-1] != '\n'
	if torn && len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}
	out := make([]Manifest, 0, len(lines))
	for i, line := range lines {
		var m Manifest
		if err := json.Unmarshal(line, &m); err != nil {
			return nil, fmt.Errorf("modelstore: journal record %d is corrupt: %w", i, err)
		}
		if err := validate(m); err != nil {
			return nil, fmt.Errorf("modelstore: journal record %d is invalid: %w", i, err)
		}
		out = append(out, m)
	}
	return out, nil
}

// Active returns the newest journaled manifest per (model, device).
func (s *Store) Active() (map[string]Manifest, error) {
	records, err := s.readRecords()
	if err != nil {
		return nil, err
	}
	out := map[string]Manifest{}
	for _, m := range records {
		out[m.Model+"/"+m.Device] = m
	}
	return out, nil
}

// AcquireLease pins the currently active manifest for (model, device) for a runtime process generation.
func (s *Store) AcquireLease(model, device, owner string, generation uint64) (*Lease, error) {
	active, err := s.Active()
	if err != nil {
		return nil, err
	}
	m, ok := active[model+"/"+device]
	if !ok {
		return nil, fmt.Errorf("modelstore: no active manifest for %s/%s", model, device)
	}
	if !s.Verified(m.Artifact.Digest) {
		return nil, fmt.Errorf("modelstore: active artifact %s is not verified", m.Artifact.Digest)
	}
	s.mu.Lock()
	s.leases[m.Artifact.Digest]++
	for _, f := range m.Files {
		s.leases[f.Digest]++
	}
	s.mu.Unlock()
	return &Lease{
		store:      s,
		Manifest:   m,
		Owner:      owner,
		Generation: generation,
	}, nil
}

func (s *Store) releaseLease(digest string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leases[digest] <= 1 {
		delete(s.leases, digest)
	} else {
		s.leases[digest]--
	}
}

// Status reports readiness and any required repair action for (model, device).
func (s *Store) Status(model, device string) Readiness {
	records, err := s.readRecords()
	if err != nil {
		return Readiness{
			State:  StateRepairRequired,
			Repair: RepairRecoverJournal,
			Reason: err.Error(),
		}
	}
	key := model + "/" + device
	var current, prev *Manifest
	for i := range records {
		m := records[i]
		if m.Model+"/"+m.Device != key {
			continue
		}
		if current != nil && current.Revision != m.Revision {
			cp := *current
			prev = &cp
		}
		cp := m
		current = &cp
	}
	if current == nil {
		return Readiness{
			State:  StateAbsent,
			Repair: RepairImportSeed,
			Reason: "model revision not activated",
		}
	}
	if !s.Verified(current.Artifact.Digest) {
		return Readiness{
			State:    StateRepairRequired,
			Active:   current,
			Previous: prev,
			Repair:   RepairRestageArtifact,
			Reason:   fmt.Sprintf("active artifact %s failed verification", current.Artifact.Digest),
		}
	}
	for _, f := range current.Files {
		if !s.Verified(f.Digest) {
			return Readiness{
				State:    StateRepairRequired,
				Active:   current,
				Previous: prev,
				Repair:   RepairRestageArtifact,
				Reason:   fmt.Sprintf("supplementary artifact %s failed verification", f.Digest),
			}
		}
	}
	return Readiness{
		State:    StateReady,
		Active:   current,
		Previous: prev,
		Repair:   RepairNone,
	}
}

// Collect recovers the journal and removes unreferenced artifacts, preserving active, previous compatible, and leased revisions.
func (s *Store) Collect() ([]string, error) {
	lock, err := s.lockJournal()
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()

	if err := s.recoverJournal(); err != nil {
		return nil, err
	}
	records, err := s.readRecords()
	if err != nil {
		return nil, err
	}

	type slotHistory struct {
		active   *Manifest
		previous *Manifest
	}
	slots := map[string]*slotHistory{}
	for i := range records {
		m := records[i]
		key := m.Model + "/" + m.Device
		sh := slots[key]
		if sh == nil {
			sh = &slotHistory{}
			slots[key] = sh
		}
		if sh.active != nil && sh.active.Revision != m.Revision {
			prev := *sh.active
			sh.previous = &prev
		}
		curr := m
		sh.active = &curr
	}

	keep := map[string]bool{}
	keepManifest := func(m *Manifest) {
		if m == nil {
			return
		}
		keep[m.Artifact.Digest] = true
		for _, f := range m.Files {
			keep[f.Digest] = true
		}
	}
	for _, sh := range slots {
		keepManifest(sh.active)
		keepManifest(sh.previous)
	}

	s.mu.Lock()
	for digest, count := range s.leases {
		if count > 0 {
			keep[digest] = true
		}
	}
	s.mu.Unlock()

	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()

	artifactsDir := filepath.Join(s.root, "artifacts")
	prefixEntries, err := os.ReadDir(artifactsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("modelstore: readdir artifacts: %w", err)
	}

	var removed []string
	for _, prefixEntry := range prefixEntries {
		if !prefixEntry.IsDir() {
			continue
		}
		prefix := prefixEntry.Name()
		subEntries, err := os.ReadDir(filepath.Join(artifactsDir, prefix))
		if err != nil {
			return nil, fmt.Errorf("modelstore: readdir %s: %w", prefix, err)
		}
		for _, entry := range subEntries {
			name := entry.Name()
			if !digestShape.MatchString(name) {
				continue
			}
			if keep[name] {
				continue
			}
			if err := root.Remove(filepath.Join("artifacts", prefix, name)); err != nil {
				return nil, fmt.Errorf("modelstore: remove %s: %w", name, err)
			}
			removed = append(removed, name)
		}
	}
	sort.Strings(removed)
	return removed, nil
}

func splitLines(raw []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range raw {
		if b == '\n' {
			if i > start {
				out = append(out, raw[start:i])
			}
			start = i + 1
		}
	}
	if start < len(raw) {
		out = append(out, raw[start:])
	}
	return out
}
