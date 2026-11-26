package native

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type recordingPythonLedger struct {
	*fakeSourceLedger
	mu      sync.Mutex
	records []sourceledger.RecordInput
}

func (r *recordingPythonLedger) Record(_ context.Context, in sourceledger.RecordInput) error {
	r.mu.Lock()
	r.records = append(r.records, in)
	r.mu.Unlock()
	return nil
}

func (r *recordingPythonLedger) Records() []sourceledger.RecordInput {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sourceledger.RecordInput, len(r.records))
	copy(out, r.records)
	return out
}

func TestPythonMutationDoorAttributesSingleAgentHistoryEntry(t *testing.T) {
	dir := t.TempDir()
	origBytes := []byte("version = 1\n")
	testutil.FailErr(t, "seed config.toml", os.WriteFile(filepath.Join(dir, "config.toml"), origBytes, 0o644))

	recLedger := &recordingPythonLedger{fakeSourceLedger: &fakeSourceLedger{}}
	tctx := provenanceCtx(dir, recLedger.fakeSourceLedger)
	tctx.Source.SourceLedger = recLedger
	tctx.Identity.SessionID = "sess-py-attr"
	tctx.Identity.Agent = "coordinator"
	tctx.Identity.ToolCallID = "call-py-attr-1"

	roots := []pythondocuments.HostRootConfig{{ID: "r1", Dir: dir}}
	door := NewPythonMutationDoor(tctx, roots)
	store := pythondocuments.NewHostDocumentStore(roots, nil, door)
	overlay := pythondocuments.NewOverlayAdapter(pythondocuments.OverlayConfig{
		ExecutionID: "exec-attr-1",
		Store:       store,
		Roots:       []pythondocuments.RootSpec{{ID: "r1", Label: "workspace", Default: true}},
	})

	ctx := context.Background()
	f, errno := overlay.Open(ctx, pythonruntime.OpenRequest{
		Path:  "config.toml",
		Flags: pythonruntime.OpenRead | pythonruntime.OpenWrite | pythonruntime.OpenTruncate,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("open config.toml: %v", errno)
	}
	updated := []byte("version = 2\n")
	if _, werr := f.Write(updated); werr != pythonruntime.ErrnoSuccess {
		t.Fatalf("write config.toml: %v", werr)
	}
	_ = f.Close()

	report, err := overlay.Settle(ctx, true)
	testutil.FailErr(t, "settle attributed python write", err)
	if len(report.Published) != 1 {
		t.Fatalf("expected 1 published change, got %+v", report.Published)
	}

	gotDisk, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	testutil.FailErr(t, "read disk config.toml", err)
	if string(gotDisk) != string(updated) {
		t.Fatalf("disk bytes = %q, want %q", gotDisk, updated)
	}

	records := recLedger.Records()
	if len(records) != 1 {
		t.Fatalf("expected exactly 1 workspace history entry, got %d (%+v)", len(records), records)
	}
	rec := records[0]
	if rec.SessionID != "sess-py-attr" || rec.ToolCallID != "call-py-attr-1" {
		t.Fatalf("unexpected attribution identity: %+v", rec)
	}
	if rec.Op != api.SourceChangeOpWrite || rec.Origin != api.SourceChangeOriginAgent {
		t.Fatalf("unexpected attribution op/origin: op=%q origin=%q", rec.Op, rec.Origin)
	}
}

func TestPythonMutationDoorModifiedBytesRenameConcurrentSourceEdit(t *testing.T) {
	t.Run("concurrent source edit before rename fails with RevisionConflict without touching destination", func(t *testing.T) {
		dir := t.TempDir()
		testutil.FailErr(t, "seed src.txt", os.WriteFile(filepath.Join(dir, "src.txt"), []byte("original\n"), 0o644))

		recLedger := &recordingPythonLedger{fakeSourceLedger: &fakeSourceLedger{}}
		tctx := provenanceCtx(dir, recLedger.fakeSourceLedger)
		tctx.Source.SourceLedger = recLedger

		roots := []pythondocuments.HostRootConfig{{ID: "r1", Dir: dir}}
		door := NewPythonMutationDoor(tctx, roots)
		store := pythondocuments.NewHostDocumentStore(roots, nil, door)
		overlay := pythondocuments.NewOverlayAdapter(pythondocuments.OverlayConfig{
			ExecutionID: "exec-rename-pre-conflict",
			Store:       store,
			Roots:       []pythondocuments.RootSpec{{ID: "r1", Label: "workspace", Default: true}},
		})

		ctx := context.Background()
		f, errno := overlay.Open(ctx, pythonruntime.OpenRequest{
			Path:  "src.txt",
			Flags: pythonruntime.OpenRead | pythonruntime.OpenWrite | pythonruntime.OpenTruncate,
		})
		if errno != pythonruntime.ErrnoSuccess {
			t.Fatalf("open src.txt: %v", errno)
		}
		_, _ = f.Write([]byte("modified-in-guest\n"))
		_ = f.Close()
		if rerr := overlay.Rename(ctx, "src.txt", "dst.txt"); rerr != pythonruntime.ErrnoSuccess {
			t.Fatalf("overlay rename: %v", rerr)
		}

		// Simulate concurrent external edit to src.txt before settlement.
		externalBytes := []byte("concurrent-external-edit\n")
		testutil.FailErr(t, "concurrent edit src.txt", os.WriteFile(filepath.Join(dir, "src.txt"), externalBytes, 0o644))

		report, err := overlay.Settle(ctx, true)
		if err == nil {
			t.Fatal("expected settlement conflict error on concurrent source edit")
		}
		if len(report.Published) != 0 {
			t.Fatalf("expected 0 published changes when source is stale, got %+v", report.Published)
		}
		if len(report.Conflicts) != 1 || report.Conflicts[0].Target.RelPath != "src.txt" {
			t.Fatalf("expected 1 conflict on src.txt, got %+v", report.Conflicts)
		}

		// Actual files on disk must match reported committed effects: dst.txt must NOT exist, src.txt must retain external edit.
		if _, statErr := os.Stat(filepath.Join(dir, "dst.txt")); !os.IsNotExist(statErr) {
			t.Fatalf("dst.txt must not exist on disk after pre-write source conflict, statErr=%v", statErr)
		}
		gotSrc, err := os.ReadFile(filepath.Join(dir, "src.txt"))
		testutil.FailErr(t, "read src.txt", err)
		if string(gotSrc) != string(externalBytes) {
			t.Fatalf("src.txt = %q, want %q", gotSrc, externalBytes)
		}
	})

	t.Run("concurrent source edit between destination write and source removal reports committed destination alongside partial failure", func(t *testing.T) {
		dir := t.TempDir()
		testutil.FailErr(t, "seed src.txt", os.WriteFile(filepath.Join(dir, "src.txt"), []byte("original\n"), 0o644))

		recLedger := &recordingPythonLedger{fakeSourceLedger: &fakeSourceLedger{}}
		tctx := provenanceCtx(dir, recLedger.fakeSourceLedger)
		tctx.Source.SourceLedger = recLedger

		roots := []pythondocuments.HostRootConfig{{ID: "r1", Dir: dir}}
		door := NewPythonMutationDoor(tctx, roots)
		externalBytes := []byte("mid-rename-external-edit\n")
		door.BeforeRemoveSource = func() {
			_ = os.WriteFile(filepath.Join(dir, "src.txt"), externalBytes, 0o644)
		}

		store := pythondocuments.NewHostDocumentStore(roots, nil, door)
		overlay := pythondocuments.NewOverlayAdapter(pythondocuments.OverlayConfig{
			ExecutionID: "exec-rename-mid-conflict",
			Store:       store,
			Roots:       []pythondocuments.RootSpec{{ID: "r1", Label: "workspace", Default: true}},
		})

		ctx := context.Background()
		f, errno := overlay.Open(ctx, pythonruntime.OpenRequest{
			Path:  "src.txt",
			Flags: pythonruntime.OpenRead | pythonruntime.OpenWrite | pythonruntime.OpenTruncate,
		})
		if errno != pythonruntime.ErrnoSuccess {
			t.Fatalf("open src.txt: %v", errno)
		}
		guestBytes := []byte("modified-in-guest\n")
		_, _ = f.Write(guestBytes)
		_ = f.Close()
		if rerr := overlay.Rename(ctx, "src.txt", "dst.txt"); rerr != pythonruntime.ErrnoSuccess {
			t.Fatalf("overlay rename: %v", rerr)
		}

		report, err := overlay.Settle(ctx, true)
		if err == nil {
			t.Fatal("expected partial failure error when source changes mid-rename")
		}
		if report.PartialFailure == nil {
			t.Fatal("expected report.PartialFailure != nil")
		}
		if len(report.Conflicts) != 1 || report.Conflicts[0].Target.RelPath != "src.txt" {
			t.Fatalf("expected 1 conflict on src.txt, got %+v", report.Conflicts)
		}
		if len(report.Published) != 1 || report.Published[0].Target.RelPath != "dst.txt" {
			t.Fatalf("expected committed destination dst.txt to be preserved in report.Published, got %+v", report.Published)
		}

		// Actual files on disk must agree with reported committed effects:
		// dst.txt has guestBytes (reported in Published), src.txt still exists with externalBytes (reported in Conflicts).
		gotDst, err := os.ReadFile(filepath.Join(dir, "dst.txt"))
		testutil.FailErr(t, "read dst.txt", err)
		if string(gotDst) != string(guestBytes) {
			t.Fatalf("dst.txt = %q, want %q", gotDst, guestBytes)
		}
		gotSrc, err := os.ReadFile(filepath.Join(dir, "src.txt"))
		testutil.FailErr(t, "read src.txt", err)
		if string(gotSrc) != string(externalBytes) {
			t.Fatalf("src.txt = %q, want %q", gotSrc, externalBytes)
		}
	})
}
