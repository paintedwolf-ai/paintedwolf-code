package modelstore

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func publish(t *testing.T, s *Store, revision, payload string) Manifest {
	t.Helper()
	a := testArtifact(t, []byte(payload))
	if err := s.Stage(bytes.NewBufferString(payload), a); err != nil {
		t.Fatalf("stage %s: %v", revision, err)
	}
	m := Manifest{Model: "e4b", Device: "mlx", Revision: revision, Artifact: a}
	if err := s.Activate(SignedManifest{Manifest: m}); err != nil {
		t.Fatalf("activate %s: %v", revision, err)
	}
	return m
}

func TestRevisionLeaseSurvivesAnotherStoreCollector(t *testing.T) {
	s := newStore(t)
	first := publish(t, s, "r1", "first")
	lease, err := s.AcquireLease("e4b", "mlx", "runner", 1)
	if err != nil {
		t.Fatalf("lease r1: %v", err)
	}
	defer lease.Release()
	collector := New(s.root, acceptSigner{})
	publish(t, collector, "r2", "second")
	publish(t, collector, "r3", "third")
	if _, err := collector.Collect(); err != nil {
		t.Fatalf("collect leased: %v", err)
	}
	if _, err := os.Stat(filepath.Join(lease.Dir, "model.safetensors")); err != nil {
		t.Fatalf("leased revision removed: %v", err)
	}
	lease.Release()
	lease.Release()
	if _, err := collector.Collect(); err != nil {
		t.Fatalf("collect released: %v", err)
	}
	if _, err := os.Stat(s.RevisionDir(first)); !os.IsNotExist(err) {
		t.Fatalf("released revision retained: %v", err)
	}
}

func TestRevisionCannotBeReboundOrRematerialized(t *testing.T) {
	s := newStore(t)
	first := publish(t, s, "r1", "first")
	path := filepath.Join(s.RevisionDir(first), "model.safetensors")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat original: %v", err)
	}
	lease, err := s.AcquireLease("e4b", "mlx", "runner", 1)
	if err != nil {
		t.Fatalf("acquire original: %v", err)
	}
	defer lease.Release()
	if err := s.Activate(SignedManifest{Manifest: first}); err != nil {
		t.Fatalf("reactivate identical: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("identical revision was replaced: %v", err)
	}
	other := testArtifact(t, []byte("different"))
	if err := s.Stage(bytes.NewBufferString("different"), other); err != nil {
		t.Fatalf("stage collision: %v", err)
	}
	collision := first
	collision.Artifact = other
	if err := s.Activate(SignedManifest{Manifest: collision}); err == nil {
		t.Fatal("revision accepted different payload")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "first" {
		t.Fatalf("original payload changed: %q, %v", raw, err)
	}
}

func TestDuplicateMaterializedNamesAreRejected(t *testing.T) {
	s := newStore(t)
	first := testArtifact(t, []byte("first"))
	second := testArtifact(t, []byte("second"))
	m := Manifest{Model: "e4b", Device: "mlx", Revision: "r1", Artifact: first, Files: []Artifact{second}}
	if err := s.Activate(SignedManifest{Manifest: m}); err == nil {
		t.Fatal("duplicate inferred filename accepted")
	}
	first.Name, second.Name = "weights", "weights"
	m.Artifact, m.Files = first, []Artifact{second}
	if err := s.Activate(SignedManifest{Manifest: m}); err == nil {
		t.Fatal("duplicate explicit filename accepted")
	}
}

func TestPersistedManifestAuthorityIsRechecked(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate fixture key: %v", err)
	}
	authority, err := NewAuthority(map[string]ed25519.PublicKey{"fixture": public})
	if err != nil {
		t.Fatalf("fixture authority: %v", err)
	}
	s := New(t.TempDir(), authority)
	payload := []byte("signed weights")
	a := testArtifact(t, payload)
	if err := s.Stage(bytes.NewReader(payload), a); err != nil {
		t.Fatalf("stage signed weights: %v", err)
	}
	m := Manifest{Model: "e4b", Device: "mlx", Revision: "r1", Artifact: a}
	raw, err := SigningBytes(m)
	if err != nil {
		t.Fatalf("signing bytes: %v", err)
	}
	signed := SignedManifest{Manifest: m, KeyID: "fixture", Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(private, raw))}
	if err := s.Activate(signed); err != nil {
		t.Fatalf("activate signed: %v", err)
	}
	if _, err := New(s.root, authority).Active(); err != nil {
		t.Fatalf("reopen signed: %v", err)
	}
	signed.Manifest.Revision = "forged"
	forged, err := json.Marshal(journalRecord{Version: 1, Signed: signed})
	if err != nil {
		t.Fatalf("encode forgery: %v", err)
	}
	if err := os.WriteFile(s.journalPath(), append(forged, '\n'), 0o600); err != nil {
		t.Fatalf("write forgery: %v", err)
	}
	if _, err := s.Active(); err == nil {
		t.Fatal("persisted unsigned mutation was accepted")
	}
	if _, err := s.Collect(); err == nil {
		t.Fatal("collector accepted unauthenticated history")
	}
}
