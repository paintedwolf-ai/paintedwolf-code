package modelstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAuditPinsMaterializedContent(t *testing.T) {
	s := newStore(t)
	m := publish(t, s, "r1", "weights")
	lease, err := s.AcquireLease("e4b", "mlx", "audit", 1)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	defer lease.Release()
	if err := lease.Audit(context.Background()); err != nil {
		t.Fatalf("audit valid revision: %v", err)
	}
	info, err := os.Stat(filepath.Join(lease.Dir, "model.safetensors"))
	if err != nil || info.Mode().Perm() != 0o400 {
		t.Fatalf("payload permissions: %v, %v", info, err)
	}
	// Replacing the artifact index must not redirect a pinned revision audit.
	if err := s.Stage(newReader([]byte("weights")), m.Artifact); err != nil {
		t.Fatalf("replace artifact: %v", err)
	}
	path := filepath.Join(s.root, s.artifactRel(m.Artifact.Digest))
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("make index fixture writable: %v", err)
	}
	if err := os.WriteFile(path, []byte("damaged"), 0o600); err != nil {
		t.Fatalf("damage index: %v", err)
	}
	if err := lease.Audit(context.Background()); err != nil {
		t.Fatalf("audit pinned revision: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := lease.Audit(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled audit: %v", err)
	}
	lease.Release()
	if err := lease.Audit(context.Background()); err == nil {
		t.Fatal("audited released lease")
	}
	var absent *Lease
	if err := absent.Audit(context.Background()); err == nil {
		t.Fatal("audited absent lease")
	}
}
