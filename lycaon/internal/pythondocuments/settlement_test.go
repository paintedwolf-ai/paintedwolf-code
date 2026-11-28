package pythondocuments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestUncaughtExceptionDiscardsOverlayWithZeroHostCalls(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryDocumentStore()
	store.SetSaved("r1", "", "out.json", []byte(`{"orig":true}`), 0o644)

	adapter := NewOverlayAdapter(OverlayConfig{
		ExecutionID: "exec-uncaught",
		Store:       store,
		Roots:       []RootSpec{{ID: "r1", Label: "main", Default: true}},
	})

	// Simulate json.dump writing a partial prefix then raising TypeError (uncaught).
	w, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "out.json",
		Flags: pythonruntime.OpenWrite | pythonruntime.OpenTruncate,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open out.json: %s", errno)
	}
	_, _ = w.Write([]byte(`{"partial": `))
	_ = w.Close()

	// Also stage a new temp file that should be discarded.
	tmp, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "staged-xyz.tmp",
		Flags: pythonruntime.OpenWrite | pythonruntime.OpenCreate,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open staged temp: %s", errno)
	}
	_, _ = tmp.Write([]byte("temp"))
	_ = tmp.Close()

	// Uncaught exception / nonzero exit / trap / os._exit(0) -> cleanCompletion=false.
	report, err := adapter.Settle(ctx, false)
	testutil.FailErr(t, "Settle cleanCompletion=false", err)
	if report.CleanCompletion {
		t.Fatal("report.CleanCompletion = true, want false")
	}
	if len(report.Published) != 0 {
		t.Fatalf("expected 0 published changes, got %d", len(report.Published))
	}
	if len(report.Discarded) != 2 {
		t.Fatalf("expected 2 discarded targets, got %+v", report.Discarded)
	}
	if calls := store.PublishLog(); len(calls) != 0 {
		t.Fatalf("expected ZERO host publish calls on failed completion, got %d", len(calls))
	}
	if saved, _ := store.SavedBytes("r1", "", "out.json"); string(saved) != `{"orig":true}` {
		t.Fatalf("host file corrupted on uncaught exception: got %q", string(saved))
	}
}

func TestCaughtExceptionPartialWritePublishesOnCleanCompletion(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryDocumentStore()
	store.SetSaved("r1", "", "partial.txt", []byte("before"), 0o644)

	adapter := NewOverlayAdapter(OverlayConfig{
		Store: store,
		Roots: []RootSpec{{ID: "r1", Label: "main", Default: true}},
	})

	// User code writes partial bytes, catches exception, then completes cleanly.
	w, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "partial.txt",
		Flags: pythonruntime.OpenWrite | pythonruntime.OpenTruncate,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open partial.txt: %s", errno)
	}
	_, _ = w.Write([]byte("partial"))
	_ = w.Close()

	report, err := adapter.Settle(ctx, true)
	testutil.FailErr(t, "Settle caught exception clean completion", err)
	if len(report.Published) != 1 || report.Published[0].Target.RelPath != "partial.txt" {
		t.Fatalf("unexpected settlement report: %+v", report)
	}
	if saved, _ := store.SavedBytes("r1", "", "partial.txt"); string(saved) != "partial" {
		t.Fatalf("host disk = %q, want %q", string(saved), "partial")
	}
}

func TestArbitrarySameDirTempRenameCollapsesToOneDestinationUpdate(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryDocumentStore()
	store.SetUnsavedDraft("r1", "", "manifest.toml", []byte("draft"))

	adapter := NewOverlayAdapter(OverlayConfig{
		Store: store,
		Roots: []RootSpec{{ID: "r1", Label: "main", Default: true}},
	})

	// Read unsaved draft, compute "DRAFT", write to arbitrary random temp name, os.replace.
	r, errno := adapter.Open(ctx, pythonruntime.OpenRequest{Path: "manifest.toml", Flags: pythonruntime.OpenRead})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open read: %s", errno)
	}
	if got := string(readAllGuest(t, r)); got != "draft" {
		t.Fatalf("read = %q, want %q", got, "draft")
	}
	_ = r.Close()

	tmpName := ".manifest_tmp_9f8a7b6c.part"
	w, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  tmpName,
		Flags: pythonruntime.OpenWrite | pythonruntime.OpenCreate | pythonruntime.OpenExclusive,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open temp: %s", errno)
	}
	_, _ = w.Write([]byte("DRAFT"))
	_ = w.Close()

	if errno := adapter.Rename(ctx, tmpName, "manifest.toml"); errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Rename temp->manifest.toml: %s", errno)
	}

	report, err := adapter.Settle(ctx, true)
	testutil.FailErr(t, "Settle temp replace", err)
	if len(report.Published) != 1 {
		t.Fatalf("expected 1 collapsed destination update, got %+v", report.Published)
	}
	if report.Published[0].Operation != "write" || report.Published[0].Target.RelPath != "manifest.toml" {
		t.Fatalf("unexpected published change: %+v", report.Published[0])
	}
	if calls := store.PublishLog(); len(calls) != 1 || calls[0].Target.RelPath != "manifest.toml" {
		t.Fatalf("expected 1 host publish call for manifest.toml, got %+v", calls)
	}
	if saved, _ := store.SavedBytes("r1", "", "manifest.toml"); string(saved) != "DRAFT" {
		t.Fatalf("manifest.toml = %q, want %q", string(saved), "DRAFT")
	}
	if _, exists := store.SavedBytes("r1", "", tmpName); exists {
		t.Fatalf("temp file %q leaked to host store", tmpName)
	}
}

func TestMoveExistingWorkspaceSourcePreservesMoveSemantics(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryDocumentStore()
	store.SetSaved("r1", "", "old.txt", []byte("original"), 0o644)

	adapter := NewOverlayAdapter(OverlayConfig{
		Store: store,
		Roots: []RootSpec{{ID: "r1", Label: "main", Default: true}},
	})

	if errno := adapter.Rename(ctx, "old.txt", "moved.txt"); errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Rename old->moved: %s", errno)
	}

	report, err := adapter.Settle(ctx, true)
	testutil.FailErr(t, "Settle move", err)
	if len(report.Published) != 1 {
		t.Fatalf("expected 1 move publication, got %+v", report.Published)
	}
	ch := report.Published[0]
	if ch.Operation != "move" || ch.Target.RelPath != "moved.txt" || ch.PreviousTarget == nil || ch.PreviousTarget.RelPath != "old.txt" {
		t.Fatalf("unexpected move PublishedChange: %+v", ch)
	}
	if _, exists := store.SavedBytes("r1", "", "old.txt"); exists {
		t.Fatal("old.txt still exists after move")
	}
	if saved, ok := store.SavedBytes("r1", "", "moved.txt"); !ok || string(saved) != "original" {
		t.Fatalf("moved.txt = %q (ok=%v), want %q", string(saved), ok, "original")
	}
}

func TestPartialMultiFileCommitFifthTargetConflicts(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryDocumentStore()
	for i := 1; i <= 5; i++ {
		store.SetSaved("r1", "", fmt.Sprintf("f%d.txt", i), []byte(fmt.Sprintf("orig-%d", i)), 0o644)
	}

	evidence := NewEvidenceLog()
	adapter := NewOverlayAdapter(OverlayConfig{
		ExecutionID: "exec-partial",
		Store:       store,
		Roots:       []RootSpec{{ID: "r1", Label: "main", Default: true}},
		Evidence:    evidence,
	})

	// Modify all 5 files in order f1..f5.
	for i := 1; i <= 5; i++ {
		name := fmt.Sprintf("f%d.txt", i)
		w, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
			Path:  name,
			Flags: pythonruntime.OpenWrite | pythonruntime.OpenTruncate,
		})
		if errno != pythonruntime.ErrnoSuccess {
			t.Fatalf("Open %s: %s", name, errno)
		}
		_, _ = w.Write([]byte(fmt.Sprintf("updated-%d", i)))
		_ = w.Close()
	}

	// Deterministic mid-settlement hook: right before f5.txt publishes, a human edits f5.txt!
	store.BeforePublish = func(_ string, target pythonruntime.CanonicalTarget) {
		if target.RelPath == "f5.txt" {
			store.SimulateHumanEdit("r1", "", "f5.txt", []byte("human-edit-5"))
		}
	}

	report, err := adapter.Settle(ctx, true)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict on 5th target, got %v", err)
	}
	if report.PartialFailure == nil {
		t.Fatal("expected report.PartialFailure to be non-nil")
	}
	if len(report.Published) != 4 {
		t.Fatalf("expected 4 earlier targets committed, got %d: %+v", len(report.Published), report.Published)
	}
	if len(report.Conflicts) != 1 || report.Conflicts[0].Target.RelPath != "f5.txt" {
		t.Fatalf("expected 1 conflict on f5.txt, got %+v", report.Conflicts)
	}

	// Verify f1..f4 survived on host and f5 preserved the human edit.
	for i := 1; i <= 4; i++ {
		name := fmt.Sprintf("f%d.txt", i)
		if saved, _ := store.SavedBytes("r1", "", name); string(saved) != fmt.Sprintf("updated-%d", i) {
			t.Fatalf("%s = %q, want %q", name, string(saved), fmt.Sprintf("updated-%d", i))
		}
	}
	if eff5, _ := store.EffectiveBytes("r1", "", "f5.txt"); string(eff5) != "human-edit-5" {
		t.Fatalf("f5.txt = %q, want %q", string(eff5), "human-edit-5")
	}

	// Verify PY05 PublicationRecords reflect 4 commits + 1 partial-failure conflict and marshal cleanly.
	pubs := evidence.Publications()
	if len(pubs) != 5 {
		t.Fatalf("expected 5 PublicationRecords, got %d", len(pubs))
	}
	if !pubs[4].PartialFailure || pubs[4].Conflict == nil || pubs[4].CommittedRevision != nil {
		t.Fatalf("unexpected 5th PublicationRecord: %+v", pubs[4])
	}
	raw, err := json.Marshal(pubs[4])
	testutil.FailErr(t, "marshal PublicationRecord", err)
	var decoded PublicationRecord
	testutil.FailErr(t, "unmarshal PublicationRecord", json.Unmarshal(raw, &decoded))
	if !decoded.PartialFailure || decoded.Conflict == nil || decoded.Target.RelPath != "f5.txt" {
		t.Fatalf("decoded PublicationRecord mismatch: %+v", decoded)
	}
}
