package modelstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestRepairRequiresReleasedRevisionAndPreservesIdentity(t *testing.T) {
	s := newStore(t)
	m := publish(t, s, "r1", "weights")
	reader := func(context.Context, Artifact) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewBufferString("weights")), nil
	}
	lease, err := s.AcquireLease("e4b", "mlx", "runner", 1)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	defer lease.Release()
	if err := s.Repair(context.Background(), SignedManifest{Manifest: m}, reader); !errors.Is(err, ErrRevisionInUse) {
		t.Fatalf("repair while leased: %v", err)
	}
	lease.Release()
	path := filepath.Join(s.RevisionDir(m), "model.safetensors")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat payload: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("make corruption fixture writable: %v", err)
	}
	if err := os.WriteFile(path, []byte("damaged"), 0600); err != nil {
		t.Fatalf("damage fixture: %v", err)
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatalf("restore payload timestamp: %v", err)
	}
	damaged, err := s.AcquireLease("e4b", "mlx", "runner", 2)
	if err != nil {
		t.Fatalf("metadata lease: %v", err)
	}
	if err := damaged.Audit(context.Background()); err == nil {
		t.Fatal("audit accepted same-size corruption with unchanged timestamp")
	}
	damaged.Release()
	if err := s.Repair(context.Background(), SignedManifest{Manifest: m}, reader); err != nil {
		t.Fatalf("repair: %v", err)
	}
	recovered, err := s.AcquireLease("e4b", "mlx", "runner", 3)
	if err != nil {
		t.Fatalf("lease repaired revision: %v", err)
	}
	defer recovered.Release()
	data, err := os.ReadFile(filepath.Join(recovered.Dir, "model.safetensors"))
	if err != nil || string(data) != "weights" || !sameManifest(recovered.Manifest, m) {
		t.Fatalf("repaired content or identity differs: %q, %v", data, err)
	}
}

func TestFailedRepairKeepsExistingDirectory(t *testing.T) {
	s := newStore(t)
	m := publish(t, s, "r1", "weights")
	before, err := os.Stat(s.RevisionDir(m))
	if err != nil {
		t.Fatalf("stat original: %v", err)
	}
	if err := os.Remove(filepath.Join(s.root, s.artifactRel(m.Artifact.Digest))); err != nil {
		t.Fatalf("remove content-addressed payload: %v", err)
	}
	err = s.Repair(context.Background(), SignedManifest{Manifest: m}, func(context.Context, Artifact) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewBufferString("invalid")), nil
	})
	if err == nil {
		t.Fatal("accepted invalid replacement")
	}
	after, err := os.Stat(s.RevisionDir(m))
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("failed repair changed published directory: %v", err)
	}
	lease, err := s.AcquireLease("e4b", "mlx", "runner", 2)
	if err != nil {
		t.Fatalf("failed repair retained an exclusive lease: %v", err)
	}
	lease.Release()
}

func TestRepairReusesVerifiedPayloadForMissingRevisionLink(t *testing.T) {
	s := newStore(t)
	m := publish(t, s, "r1", "weights")
	payload := filepath.Join(s.root, s.artifactRel(m.Artifact.Digest))
	before, err := os.Stat(payload)
	if err != nil {
		t.Fatalf("stat payload: %v", err)
	}
	if err := os.Remove(filepath.Join(s.RevisionDir(m), "model.safetensors")); err != nil {
		t.Fatalf("remove revision link: %v", err)
	}
	err = s.Repair(t.Context(), SignedManifest{Manifest: m}, func(context.Context, Artifact) (io.ReadCloser, error) {
		t.Fatal("repair requested an already verified payload")
		return nil, errors.New("unexpected source read")
	})
	if err != nil {
		t.Fatalf("repair missing link: %v", err)
	}
	after, err := os.Stat(filepath.Join(s.RevisionDir(m), "model.safetensors"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("repair did not reuse verified inode: %v", err)
	}
	lease, err := s.AcquireLease("e4b", "mlx", "runner", 2)
	if err != nil {
		t.Fatalf("lease repaired revision: %v", err)
	}
	defer lease.Release()
	if err := lease.Audit(t.Context()); err != nil {
		t.Fatalf("audit repaired revision: %v", err)
	}
}
