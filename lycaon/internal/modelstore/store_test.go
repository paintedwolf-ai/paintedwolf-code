package modelstore

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func testArtifact(t *testing.T, payload []byte) Artifact {
	t.Helper()
	sum := sha256.Sum256(payload)
	return Artifact{Digest: hex.EncodeToString(sum[:]), Format: "safetensors", Bytes: int64(len(payload))}
}

func TestStageVerifyActivate(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, "models"))
	payload := []byte("weights-bytes")
	a := testArtifact(t, payload)

	if err := s.Stage(newReader(payload), a); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if !s.Verified(a.Digest) {
		t.Fatal("staged artifact should verify")
	}

	m := Manifest{Model: "gemma4-e4b-paintedwolf", Revision: "proto-1", Device: "cpu", Artifact: a}
	if err := s.Activate(m); err != nil {
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
	dir := t.TempDir()
	s := New(filepath.Join(dir, "models"))
	a := testArtifact(t, []byte("real"))
	if err := s.Stage(newReader([]byte("wrong")), a); err == nil {
		t.Fatal("expected digest mismatch error")
	}
	if s.Verified(a.Digest) {
		t.Fatal("mismatched payload must not verify")
	}
}

func TestActivateRequiresVerifiedArtifact(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, "models"))
	m := Manifest{Model: "m", Revision: "r", Device: "cpu", Artifact: Artifact{Digest: "missing", Bytes: 1}}
	if err := s.Activate(m); err == nil {
		t.Fatal("expected activation of unverified artifact to fail")
	}
}

func TestNewestJournalEntryWins(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, "models"))
	payload := []byte("v2")
	a := testArtifact(t, payload)
	if err := s.Stage(newReader(payload), a); err != nil {
		t.Fatalf("stage: %v", err)
	}
	for _, rev := range []string{"r1", "r2"} {
		if err := s.Activate(Manifest{Model: "m", Revision: rev, Device: "cpu", Artifact: a}); err != nil {
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

func newReader(b []byte) *os.File {
	f, err := os.CreateTemp("", "payload")
	if err != nil {
		panic(err)
	}
	f.Write(b)
	f.Seek(0, 0)
	return f
}
