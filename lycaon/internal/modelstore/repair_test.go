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
	if err := os.WriteFile(path, []byte("damaged"), 0600); err != nil {
		t.Fatalf("damage fixture: %v", err)
	}
	if _, err := s.AcquireLease("e4b", "mlx", "runner", 2); err == nil {
		t.Fatal("leased damaged revision")
	}
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
