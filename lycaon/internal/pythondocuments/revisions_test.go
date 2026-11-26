package pythondocuments

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRereadCannotRefreshStalePin(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryDocumentStore()
	r1 := store.SetUnsavedDraft("root-main", "", "config.toml", []byte("mode = 'v1'\n"))

	adapter := NewOverlayAdapter(OverlayConfig{
		ExecutionID: "exec-reread",
		Store:       store,
		Roots:       []RootSpec{{ID: "root-main", Label: "main", Default: true}},
	})

	// 1. Initial read pins R1.
	f1, errno := adapter.Open(ctx, pythonruntime.OpenRequest{Path: "config.toml", Flags: pythonruntime.OpenRead})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("initial Open: %s", errno)
	}
	_ = readAllGuest(t, f1)
	_ = f1.Close()

	// 2. Human edits config.toml to R2 while program is running.
	r2 := store.SimulateHumanEdit("root-main", "", "config.toml", []byte("mode = 'human'\n"))
	if r2.Identity() == r1.Identity() {
		t.Fatalf("human edit did not advance revision: %s", r2.Identity())
	}

	// 3. A helper rereads config.toml and sees R2. This MUST NOT refresh the earliest pin (R1)!
	f2, errno := adapter.Open(ctx, pythonruntime.OpenRequest{Path: "config.toml", Flags: pythonruntime.OpenRead})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("helper reread Open: %s", errno)
	}
	if got := string(readAllGuest(t, f2)); got != "mode = 'human'\n" {
		t.Fatalf("helper reread saw %q, want human edit", got)
	}
	_ = f2.Close()

	pin, ok := adapter.Pins().Get(pythonruntime.CanonicalTarget{
		Namespace: pythonruntime.NamespaceWorkspace,
		RootID:    "root-main",
		RelPath:   "config.toml",
	})
	if !ok || pin.BaseRevision.Identity() != r1.Identity() {
		t.Fatalf("earliest pin was refreshed! got %q, want %q", pin.BaseRevision.Identity(), r1.Identity())
	}

	// 4. Program writes output computed from stale R1 via temp+replace.
	tmp, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  ".tmp-random-9876",
		Flags: pythonruntime.OpenWrite | pythonruntime.OpenCreate,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open temp: %s", errno)
	}
	_, _ = tmp.Write([]byte("mode = 'stale-derived'\n"))
	_ = tmp.Close()
	if errno := adapter.Rename(ctx, ".tmp-random-9876", "config.toml"); errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Rename temp->config.toml: %s", errno)
	}

	// 5. Settlement MUST reject with COMMAND_REVISION_CONFLICT and preserve human edit!
	report, err := adapter.Settle(ctx, true)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("Settle err = %v, want ErrRevisionConflict", err)
	}
	if len(report.Conflicts) != 1 || report.Conflicts[0].ExpectedRevision != r1.Identity() || report.Conflicts[0].ActualRevision != r2.Identity() {
		t.Fatalf("unexpected conflicts: %+v", report.Conflicts)
	}
	if eff, _ := store.EffectiveBytes("root-main", "", "config.toml"); string(eff) != "mode = 'human'\n" {
		t.Fatalf("human edit was overwritten! got %q", string(eff))
	}
}

func TestChangedThenRestoredValueStillConflicts(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryDocumentStore()
	r1 := store.SetUnsavedDraft("root-main", "", "val.txt", []byte("same"))

	adapter := NewOverlayAdapter(OverlayConfig{
		Store: store,
		Roots: []RootSpec{{ID: "root-main", Label: "main", Default: true}},
	})

	f, errno := adapter.Open(ctx, pythonruntime.OpenRequest{Path: "val.txt", Flags: pythonruntime.OpenRead})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open read: %s", errno)
	}
	_ = readAllGuest(t, f)
	_ = f.Close()

	// Human edits to "changed" (R2) and then edits back to "same" (R3).
	_ = store.SimulateHumanEdit("root-main", "", "val.txt", []byte("changed"))
	r3 := store.SimulateHumanEdit("root-main", "", "val.txt", []byte("same"))

	w, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "val.txt",
		Flags: pythonruntime.OpenWrite | pythonruntime.OpenTruncate,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open write: %s", errno)
	}
	_, _ = w.Write([]byte("agent-update"))
	_ = w.Close()

	report, err := adapter.Settle(ctx, true)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict when bytes returned to original value under new revision, got %v", err)
	}
	if len(report.Conflicts) != 1 || report.Conflicts[0].ExpectedRevision != r1.Identity() || report.Conflicts[0].ActualRevision != r3.Identity() {
		t.Fatalf("unexpected conflict details: %+v", report.Conflicts)
	}
	if eff, _ := store.EffectiveBytes("root-main", "", "val.txt"); string(eff) != "same" {
		t.Fatalf("host content mutated on conflict: got %q", string(eff))
	}
}

func TestSecondWriteAfterCommitAndInterveningHumanEdit(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryDocumentStore()
	store.SetSaved("root-main", "", "state.txt", []byte("v0"), 0o644)

	adapter := NewOverlayAdapter(OverlayConfig{
		Store: store,
		Roots: []RootSpec{{ID: "root-main", Label: "main", Default: true}},
	})

	// Write 1 + clean commit advances pin to authoritative committed revision.
	w1, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "state.txt",
		Flags: pythonruntime.OpenWrite | pythonruntime.OpenTruncate,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open w1: %s", errno)
	}
	_, _ = w1.Write([]byte("v1"))
	_ = w1.Close()
	rep1, err := adapter.Settle(ctx, true)
	testutil.FailErr(t, "Settle 1", err)
	if len(rep1.Published) != 1 {
		t.Fatalf("Settle 1 published %d changes, want 1", len(rep1.Published))
	}

	// Write 2 with NO intervening human edit succeeds using advanced pin.
	w2, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "state.txt",
		Flags: pythonruntime.OpenWrite | pythonruntime.OpenTruncate,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open w2: %s", errno)
	}
	_, _ = w2.Write([]byte("v2"))
	_ = w2.Close()
	rep2, err := adapter.Settle(ctx, true)
	testutil.FailErr(t, "Settle 2", err)
	if len(rep2.Published) != 1 {
		t.Fatalf("Settle 2 published %d changes, want 1", len(rep2.Published))
	}
	if saved, _ := store.SavedBytes("root-main", "", "state.txt"); string(saved) != "v2" {
		t.Fatalf("after commit 2, disk = %q, want %q", string(saved), "v2")
	}

	// Now a human edits state.txt BEFORE Write 3 (no reread).
	store.SimulateHumanEdit("root-main", "", "state.txt", []byte("human-v3"))

	w3, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "state.txt",
		Flags: pythonruntime.OpenWrite | pythonruntime.OpenTruncate,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open w3: %s", errno)
	}
	_, _ = w3.Write([]byte("agent-v3"))
	_ = w3.Close()

	_, err = adapter.Settle(ctx, true)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("Write 3 after intervening human edit err = %v, want ErrRevisionConflict", err)
	}
	if eff, _ := store.EffectiveBytes("root-main", "", "state.txt"); string(eff) != "human-v3" {
		t.Fatalf("human-v3 overwritten! got %q", string(eff))
	}
}

func TestTwoRootsIdenticalRelpathsAndSymlinkAliasPinning(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryDocumentStore()
	store.SetSaved("root-a", "", "config.toml", []byte("root=a"), 0o644)
	store.SetSaved("root-b", "", "config.toml", []byte("root=b"), 0o644)
	store.SetSymlink("root-a", "", "alias.toml", "config.toml")

	adapter := NewOverlayAdapter(OverlayConfig{
		Store: store,
		Roots: []RootSpec{
			{ID: "root-a", Label: "alpha", Default: true},
			{ID: "root-b", Label: "beta"},
		},
	})

	// Read via symlink alias in root-a -> pins canonical target root-a/config.toml.
	if link, errno := adapter.Readlink(ctx, "@alpha/alias.toml"); errno != pythonruntime.ErrnoSuccess || link != "config.toml" {
		t.Fatalf("Readlink = (%q, %s), want (\"config.toml\", SUCCESS)", link, errno)
	}
	lst, errno := adapter.Lstat(ctx, "@alpha/alias.toml")
	if errno != pythonruntime.ErrnoSuccess || !lst.IsSymlink {
		t.Fatalf("Lstat symlink = (%+v, %s)", lst, errno)
	}
	fAlias, errno := adapter.Open(ctx, pythonruntime.OpenRequest{Path: "@alpha/alias.toml", Flags: pythonruntime.OpenRead})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open symlink alias: %s", errno)
	}
	if got := string(readAllGuest(t, fAlias)); got != "root=a" {
		t.Fatalf("alias read = %q, want %q", got, "root=a")
	}
	_ = fAlias.Close()

	// Read root-b/config.toml -> pins root-b/config.toml independently.
	fB, errno := adapter.Open(ctx, pythonruntime.OpenRequest{Path: "@beta/config.toml", Flags: pythonruntime.OpenRead})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open @beta/config.toml: %s", errno)
	}
	if got := string(readAllGuest(t, fB)); got != "root=b" {
		t.Fatalf("@beta read = %q, want %q", got, "root=b")
	}
	_ = fB.Close()

	// Human edits ONLY root-a/config.toml.
	store.SimulateHumanEdit("root-a", "", "config.toml", []byte("root=a-human"))

	// Write to root-b/config.toml MUST succeed (distinct canonical target).
	wB, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "@beta/config.toml",
		Flags: pythonruntime.OpenWrite | pythonruntime.OpenTruncate,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open write @beta: %s", errno)
	}
	_, _ = wB.Write([]byte("root=b-updated"))
	_ = wB.Close()
	_, err := adapter.Settle(ctx, true)
	testutil.FailErr(t, "Settle @beta", err)
	if saved, _ := store.SavedBytes("root-b", "", "config.toml"); string(saved) != "root=b-updated" {
		t.Fatalf("@beta/config.toml = %q, want %q", string(saved), "root=b-updated")
	}

	// Write to root-a/config.toml directly MUST conflict against the pin installed via alias.toml!
	wA, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "@alpha/config.toml",
		Flags: pythonruntime.OpenWrite | pythonruntime.OpenTruncate,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open write @alpha: %s", errno)
	}
	_, _ = wA.Write([]byte("root=a-stale"))
	_ = wA.Close()
	if _, err := adapter.Settle(ctx, true); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected conflict on @alpha/config.toml via symlink pin, got %v", err)
	}
}

func TestReplacementByNewObjectConflicts(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryDocumentStore()
	store.SetSaved("r1", "", "obj.txt", []byte("same-bytes"), 0o644)

	adapter := NewOverlayAdapter(OverlayConfig{
		Store: store,
		Roots: []RootSpec{{ID: "r1", Label: "main", Default: true}},
	})

	f, errno := adapter.Open(ctx, pythonruntime.OpenRequest{Path: "obj.txt", Flags: pythonruntime.OpenRead})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open read: %s", errno)
	}
	_ = readAllGuest(t, f)
	_ = f.Close()

	// Replace obj.txt with a brand-new document object holding identical bytes.
	store.ReplaceWithNewObject("r1", "", "obj.txt", []byte("same-bytes"))

	w, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "obj.txt",
		Flags: pythonruntime.OpenWrite | pythonruntime.OpenTruncate,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open write: %s", errno)
	}
	_, _ = w.Write([]byte("new-bytes"))
	_ = w.Close()

	if _, err := adapter.Settle(ctx, true); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict after object replacement, got %v", err)
	}
}
