package modelstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"testing"
)

func TestImportPinsStagingAcrossCollection(t *testing.T) {
	s := newStore(t)
	a := testArtifact(t, []byte("weights"))
	b := testArtifact(t, []byte("configuration"))
	b.Name = "config.json"
	m := Manifest{Model: "e4b", Device: "mlx", Revision: "r1", Artifact: a, Files: []Artifact{b}}
	payloads := map[string]string{a.Digest: "weights", b.Digest: "configuration"}
	first := ""
	err := s.Import(context.Background(), SignedManifest{Manifest: m}, func(_ context.Context, a Artifact) (io.ReadCloser, error) {
		if first != "" {
			collector := New(s.root, acceptSigner{})
			if _, err := collector.Collect(); err != nil {
				return nil, err
			}
			if !collector.Verified(first) {
				t.Fatal("collector removed an import's staged payload")
			}
		}
		first = a.Digest
		return io.NopCloser(bytes.NewBufferString(payloads[a.Digest])), nil
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	lease, err := s.AcquireLease("e4b", "mlx", "runner", 1)
	if err != nil {
		t.Fatalf("lease imported revision: %v", err)
	}
	lease.Release()
}

func TestCanceledImportPreservesActiveRevision(t *testing.T) {
	s := newStore(t)
	publish(t, s, "working", "working")
	ctx, cancel := context.WithCancel(context.Background())
	a := testArtifact(t, []byte("replacement"))
	m := Manifest{Model: "e4b", Device: "mlx", Revision: "replacement", Artifact: a}
	err := s.Import(ctx, SignedManifest{Manifest: m}, func(context.Context, Artifact) (io.ReadCloser, error) {
		cancel()
		return io.NopCloser(bytes.NewBufferString("replacement")), nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled import: %v", err)
	}
	state := s.Status("e4b", "mlx")
	if state.State != StateReady || state.Active.Revision != "working" {
		t.Fatalf("active revision changed: %+v", state)
	}
}

func TestDiskBudgetRejectsNegativeAndOverflow(t *testing.T) {
	s := newStore(t)
	for _, req := range []SpaceRequirement{{StagingBytes: -1}, {StagingBytes: math.MaxInt64, UnpackingBytes: 1}} {
		if err := s.CheckSpace(req); err == nil {
			t.Fatalf("accepted disk budget: %+v", req)
		}
	}
}
