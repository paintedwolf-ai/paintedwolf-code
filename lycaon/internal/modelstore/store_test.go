package modelstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// acceptSigner stands in for the host's real manifest authority in tests.
type acceptSigner struct{ fail bool }

func (a acceptSigner) Verify(SignedManifest) error {
	if a.fail {
		return errors.New("signature rejected")
	}
	return nil
}

func testArtifact(t *testing.T, payload []byte) Artifact {
	t.Helper()
	sum := sha256.Sum256(payload)
	return Artifact{Digest: hex.EncodeToString(sum[:]), Format: "safetensors", Bytes: int64(len(payload))}
}

func newStore(t *testing.T) *Store {
	t.Helper()
	return New(filepath.Join(t.TempDir(), "models"), acceptSigner{})
}

func TestStageVerifyActivate(t *testing.T) {
	s := newStore(t)
	payload := []byte("weights-bytes")
	a := testArtifact(t, payload)

	if err := s.Stage(newReader(payload), a); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if !s.Verified(a.Digest) {
		t.Fatal("staged artifact should verify")
	}

	m := Manifest{Model: "gemma4-e4b-paintedwolf", Revision: "proto-1", Device: "cpu", Artifact: a}
	if err := s.Activate(SignedManifest{Manifest: m}); err != nil {
		t.Fatalf("activate: %v", err)
	}
	active, err := s.Active()
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if got := active["gemma4-e4b-paintedwolf/cpu"]; got.Revision != "proto-1" {
		t.Fatalf("revision: got %q", got.Revision)
	}
}

func TestDigestMismatchLeavesNothing(t *testing.T) {
	s := newStore(t)
	a := testArtifact(t, []byte("real"))
	if err := s.Stage(newReader([]byte("wrong")), a); err == nil {
		t.Fatal("expected digest mismatch error")
	}
	if s.Verified(a.Digest) {
		t.Fatal("mismatched payload must not verify")
	}
}

func TestOversizeInputRejected(t *testing.T) {
	s := newStore(t)
	a := testArtifact(t, []byte("real"))
	a.Bytes = 1 // declared smaller than the source
	if err := s.Stage(newReader([]byte("too long")), a); err == nil {
		t.Fatal("expected size mismatch error")
	}
}

func TestBadIdentityRejected(t *testing.T) {
	s := newStore(t)
	// A digest shorter than two characters panicked before validation existed.
	for _, digest := range []string{"", "x", "../escape", "a\nb"} {
		a := Artifact{Digest: digest, Bytes: 4}
		if err := s.Stage(newReader([]byte("data")), a); err == nil {
			t.Fatalf("digest %q: expected rejection", digest)
		}
	}
	m := Manifest{Model: "../escape", Revision: "r", Device: "cpu", Artifact: Artifact{Digest: "missing", Bytes: 1}}
	if err := s.Activate(SignedManifest{Manifest: m}); err == nil {
		t.Fatal("expected traversal identity rejection")
	}
}

func TestActivateRequiresSignedManifest(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, "models"), acceptSigner{fail: true})
	payload := []byte("weights")
	a := testArtifact(t, payload)
	if err := s.Stage(newReader(payload), a); err != nil {
		t.Fatalf("stage: %v", err)
	}
	m := Manifest{Model: "m", Revision: "r", Device: "cpu", Artifact: a}
	if err := s.Activate(SignedManifest{Manifest: m}); err == nil {
		t.Fatal("expected unsigned manifest rejection")
	}
}

func TestActivateRequiresVerifiedArtifact(t *testing.T) {
	s := newStore(t)
	m := Manifest{Model: "m", Revision: "r", Device: "cpu", Artifact: Artifact{Digest: missingDigest(), Bytes: 1}}
	if err := s.Activate(SignedManifest{Manifest: m}); err == nil {
		t.Fatal("expected activation of unverified artifact to fail")
	}
}

func missingDigest() string {
	return hex.EncodeToString(make([]byte, 32))
}

func TestNewestJournalEntryWins(t *testing.T) {
	s := newStore(t)
	payload := []byte("v2")
	a := testArtifact(t, payload)
	if err := s.Stage(newReader(payload), a); err != nil {
		t.Fatalf("stage: %v", err)
	}
	for _, rev := range []string{"r1", "r2"} {
		if err := s.Activate(SignedManifest{Manifest: Manifest{Model: "m", Revision: rev, Device: "cpu", Artifact: a}}); err != nil {
			t.Fatalf("activate %s: %v", rev, err)
		}
	}
	active, err := s.Active()
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if got := active["m/cpu"].Revision; got != "r2" {
		t.Fatalf("newest revision: got %q want r2", got)
	}
}

func TestTornTailRecoversAndNextActivateSurvives(t *testing.T) {
	s := newStore(t)
	payload := []byte("weights")
	a := testArtifact(t, payload)
	if err := s.Stage(newReader(payload), a); err != nil {
		t.Fatalf("stage: %v", err)
	}
	m := Manifest{Model: "m", Revision: "r1", Device: "cpu", Artifact: a}
	if err := s.Activate(SignedManifest{Manifest: m}); err != nil {
		t.Fatalf("activate: %v", err)
	}
	// Simulate a crash mid-append.
	f, err := os.OpenFile(s.journalPath(), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	f.Write([]byte(`{"model":"m","revi`))
	f.Close()

	// The next activation repairs the tail and its own entry survives.
	m2 := Manifest{Model: "m", Revision: "r2", Device: "cpu", Artifact: a}
	if err := s.Activate(SignedManifest{Manifest: m2}); err != nil {
		t.Fatalf("activate after tear: %v", err)
	}
	active, err := s.Active()
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if got := active["m/cpu"].Revision; got != "r2" {
		t.Fatalf("revision after recovery: got %q want r2", got)
	}
}

func TestCorruptMiddleIsAnError(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		t.Fatalf("root: %v", err)
	}
	if err := os.WriteFile(s.journalPath(), []byte("not json\n"), 0o644); err != nil {
		t.Fatalf("seed journal: %v", err)
	}
	m := Manifest{Model: "m", Revision: "r", Device: "cpu", Artifact: Artifact{Digest: missingDigest(), Bytes: 1}}
	if err := s.Activate(SignedManifest{Manifest: m}); err == nil {
		t.Fatal("expected corruption before the tail to fail activation")
	}
}

func newReader(b []byte) *os.File {
	f, err := os.CreateTemp("", "payload")
	if err != nil {
		panic(err)
	}
	f.Write(b)
	f.Seek(0, 0)
	return f
}

func TestSymlinkEscapeRejected(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		t.Fatalf("root: %v", err)
	}
	outside := t.TempDir()
	// A symlinked `artifacts` prefix must not redirect staging outside the store.
	if err := os.Symlink(outside, filepath.Join(s.root, "artifacts")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	payload := []byte("weights-bytes")
	a := testArtifact(t, payload)
	if err := s.Stage(newReader(payload), a); err == nil {
		t.Fatal("expected symlinked artifacts dir to fail staging")
	}
	if _, err := os.Lstat(filepath.Join(outside, a.Digest[:2])); !os.IsNotExist(err) {
		t.Fatal("staging escaped the store")
	}
	if s.Verified(a.Digest) {
		t.Fatal("verified followed a symlink out of the store")
	}
}

func TestCorruptCompleteRecordFailsActive(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		t.Fatalf("root: %v", err)
	}
	payload := []byte("weights-bytes")
	a := testArtifact(t, payload)
	if err := s.Stage(newReader(payload), a); err != nil {
		t.Fatalf("stage: %v", err)
	}
	m := Manifest{Model: "m", Revision: "r", Device: "cpu", Artifact: a}
	if err := s.Activate(SignedManifest{Manifest: m}); err != nil {
		t.Fatalf("activate: %v", err)
	}
	// Corrupt a complete record: the trailing newline stays, so recovery and
	// Active must both detect it rather than fall back to older state.
	raw, err := os.ReadFile(s.journalPath())
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if err := os.WriteFile(s.journalPath(), append([]byte("garbage"), raw...), 0o644); err != nil {
		t.Fatalf("corrupt journal: %v", err)
	}
	if _, err := s.Active(); err == nil {
		t.Fatal("expected Active to reject a corrupt complete record")
	}
	if err := s.Activate(SignedManifest{Manifest: Manifest{Model: "m", Revision: "r2", Device: "cpu", Artifact: a}}); err == nil {
		t.Fatal("expected Activate to reject a corrupt complete record")
	}
}
