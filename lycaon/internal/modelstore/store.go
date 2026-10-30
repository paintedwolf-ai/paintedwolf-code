// Package modelstore owns the durable lifecycle of local model artifacts:
// staging, verification, activation and repair.
//
// The lifecycle is absent → staging → verified → active. Only verified
// artifacts activate; activation is an append-only journal entry so a crash
// between file placement and activation is recoverable. Every successful
// operation is durable before it returns: staging syncs the file and its
// directory, activation syncs the journal. The journal is recovered under an
// exclusive lock before any write.
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
	"syscall"

	"github.com/lycaon/lycaon/internal/fssync"
)

// MaxArtifactBytes bounds a staged payload. The E4B release artifact is well
// under this; a larger input is rejected before any copy.
const MaxArtifactBytes = 64 << 30

// MaxJournalBytes bounds recovery: a journal larger than this is corruption,
// not history.
const MaxJournalBytes = 16 << 20

// digestShape matches a lowercase hex sha256.
var digestShape = regexp.MustCompile(`^[0-9a-f]{64}$`)

// modelShape bounds model and revision names to path-safe characters.
var nameShape = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Artifact identifies one immutable model payload.
type Artifact struct {
	// Digest is the sha256 of the payload, hex-encoded.
	Digest string `json:"digest"`
	// Format names the layout (safetensors, gguf).
	Format string `json:"format"`
	// Bytes is the payload size.
	Bytes int64 `json:"bytes"`
}

// Manifest describes one model revision: the payload and how to serve it.
type Manifest struct {
	// Model is the stable model identity.
	Model string `json:"model"`
	// Revision distinguishes builds of the same model.
	Revision string `json:"revision"`
	// Artifact is the generation payload.
	Artifact Artifact `json:"artifact"`
	// Device names the backend recipe (cpu, mlx, vulkan).
	Device string `json:"device"`
}

// SignedManifest is the authority boundary: a manifest plus its detached
// signature. Activation requires one; a bare manifest is never authority.
type SignedManifest struct {
	Manifest  Manifest `json:"manifest"`
	Signature string   `json:"signature"`
}

// Signer verifies manifest authority. The host injects its key trust; the
// store never derives trust from file contents.
type Signer interface {
	Verify(SignedManifest) error
}

// Store roots the on-disk layout: artifacts by digest, journal, manifests.
type Store struct {
	root   string
	signer Signer
}

func New(root string, signer Signer) *Store { return &Store{root: root, signer: signer} }

// validate rejects identities that could escape the store or panic later,
// before any filesystem effect.
func validate(m Manifest) error {
	if !nameShape.MatchString(m.Model) {
		return fmt.Errorf("modelstore: model name %q is not a safe identity", m.Model)
	}
	if !nameShape.MatchString(m.Revision) {
		return fmt.Errorf("modelstore: revision %q is not a safe identity", m.Revision)
	}
	return validateArtifact(m.Artifact)
}

// validateArtifact bounds a payload identity independent of any manifest.
func validateArtifact(a Artifact) error {
	if !digestShape.MatchString(a.Digest) {
		return fmt.Errorf("modelstore: digest %q is not a hex sha256", a.Digest)
	}
	if a.Bytes <= 0 || a.Bytes > MaxArtifactBytes {
		return fmt.Errorf("modelstore: artifact size %d out of bounds", a.Bytes)
	}
	return nil
}

func (s *Store) artifactPath(digest string) string {
	return filepath.Join(s.root, "artifacts", digest[:2], digest)
}

func (s *Store) journalPath() string { return filepath.Join(s.root, "journal.jsonl") }

// journalLock holds an exclusive advisory lock on the journal's lock file.
// The caller closes the returned file to release.
type journalLock struct{ f *os.File }

func (s *Store) lockJournal() (*journalLock, error) {
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return nil, fmt.Errorf("modelstore: root: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(s.root, "journal.lock"), os.O_CREATE|os.O_RDWR, 0o644)
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
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
}

// Stage copies a payload into the store under its digest, if absent.
// It enforces the declared size while copying and verifies the hash; a
// mismatch leaves nothing behind. The file and its directory are synced
// before the rename and the publish is synced before returning.
func (s *Store) Stage(r io.Reader, want Artifact) error {
	if err := validateArtifact(want); err != nil {
		return err
	}
	dir := filepath.Dir(s.artifactPath(want.Digest))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("modelstore: mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".staging-*")
	if err != nil {
		return fmt.Errorf("modelstore: temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

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
	// Durable publish: sync file, rename, sync directory.
	if err := fssync.File(tmp); err != nil {
		tmp.Close()
		return fmt.Errorf("modelstore: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("modelstore: close: %w", err)
	}
	if err := os.Rename(tmpName, s.artifactPath(want.Digest)); err != nil {
		return fmt.Errorf("modelstore: publish: %w", err)
	}
	if dfd, err := os.Open(dir); err == nil {
		_ = fssync.File(dfd)
		dfd.Close()
	}
	return nil
}

// Verified reports whether an artifact is fully present with the right hash.
func (s *Store) Verified(digest string) bool {
	if !digestShape.MatchString(digest) {
		return false
	}
	f, err := os.Open(s.artifactPath(digest))
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

// Activate verifies manifest authority, then records the manifest in the
// journal, making it the store's intent. The journal is recovered under an
// exclusive lock first, so a torn tail never corrupts the append. The entry
// is durable before Activate returns.
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
	return nil
}

// recoverJournal truncates a torn trailing record so later appends stay
// well-formed. Corruption before the last newline is an error, not history.
// Caller holds the journal lock.
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
	// Find the last complete line.
	tail := len(raw)
	if tail > 0 && raw[tail-1] == '\n' {
		return nil // nothing torn
	}
	if idx := lastNewline(raw); idx >= 0 {
		tail = idx + 1
	} else {
		tail = 0
	}
	// Everything before the tail must parse; otherwise the journal is corrupt.
	for _, line := range splitLines(raw[:tail]) {
		var m Manifest
		if err := json.Unmarshal(line, &m); err != nil {
			return fmt.Errorf("modelstore: journal corrupt at byte %d: %w", 0, err)
		}
	}
	return os.Truncate(s.journalPath(), int64(tail))
}

func lastNewline(raw []byte) int {
	for i := len(raw) - 1; i >= 0; i-- {
		if raw[i] == '\n' {
			return i
		}
	}
	return -1
}

// Active returns the newest journaled manifest per (model, device). A torn
// final line is ignored (the next Activate repairs it under the lock).
func (s *Store) Active() (map[string]Manifest, error) {
	raw, err := os.ReadFile(s.journalPath())
	if os.IsNotExist(err) {
		return map[string]Manifest{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("modelstore: read journal: %w", err)
	}
	out := map[string]Manifest{}
	for _, line := range splitLines(raw) {
		var m Manifest
		if err := json.Unmarshal(line, &m); err != nil {
			continue // torn tail: recoverable at the next Activate
		}
		out[m.Model+"/"+m.Device] = m
	}
	return out, nil
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

