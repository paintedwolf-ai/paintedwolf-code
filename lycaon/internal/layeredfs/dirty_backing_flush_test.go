package layeredfs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDirtyBackingFilesFlushedAtCheckpointBarrierWithZeroPerOpFlushes(t *testing.T) {
	tmp := t.TempDir()
	trunk := filepath.Join(tmp, "trunk")
	backing := filepath.Join(tmp, "backing")
	testutil.FailErr(t, "mkdir trunk", os.MkdirAll(trunk, 0o755))
	testutil.FailErr(t, "mkdir backing", os.MkdirAll(backing, 0o700))
	testutil.FailErr(t, "seed lower1", os.WriteFile(filepath.Join(trunk, "lower1.txt"), []byte("seed-1\n"), 0o644))
	testutil.FailErr(t, "seed lower2", os.WriteFile(filepath.Join(trunk, "lower2.txt"), []byte("seed-2\n"), 0o644))

	core, err := New(Config{
		ViewID:     "view-ruling8-dirty-flush",
		BackingDir: backing,
		Roots:      []RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true}},
	})
	testutil.FailErr(t, "New Core", err)
	t.Cleanup(func() { _ = core.ReleaseView() })

	q := func(rel string) QualifiedPath { return QualifiedPath{RootID: "primary", Rel: rel} }

	// 1. Mutate and close working/base files; verify 0 per-operation flushes.
	_, err = core.WriteFile(q("created_closed.txt"), []byte("created\n"), 0o644)
	testutil.FailErr(t, "WriteFile created_closed.txt", err)

	// Copy-up lower1.txt (captures base-1 and writes work-2), then close handle.
	h1, _, err := core.Open(q("lower1.txt"), OpenReadWrite, 0)
	testutil.FailErr(t, "Open lower1.txt", err)
	_, err = core.WriteHandle(h1, []byte("mutated-1\n"), 0)
	testutil.FailErr(t, "WriteHandle lower1.txt", err)
	testutil.FailErr(t, "SyncHandle lower1.txt", core.SyncHandle(h1))
	testutil.FailErr(t, "CloseHandle lower1.txt", core.CloseHandle(h1))

	// Truncate lower2.txt (captures base-2 and truncates work-3).
	_, err = core.Truncate(q("lower2.txt"), 2)
	testutil.FailErr(t, "Truncate lower2.txt", err)

	// Keep one open writable handle to verify it is flushed once via held handles and not double-flushed.
	hOpen, _, err := core.CreateFile(q("still_open.txt"), 0o644, true)
	testutil.FailErr(t, "CreateFile still_open.txt", err)
	_, err = core.WriteHandle(hOpen, []byte("open-payload\n"), 0)
	testutil.FailErr(t, "WriteHandle still_open.txt", err)
	defer func() { _ = core.CloseHandle(hOpen) }()

	if got := core.Stats().Flushes; got != 0 {
		t.Fatalf("expected 0 flushes during mutations and client sync, got %d", got)
	}

	core.mu.RLock()
	dirtyBefore := len(core.state.dirtyBackingFiles)
	core.mu.RUnlock()
	// Expected dirty files: work for created_closed.txt, base+work for lower1.txt, base+work for lower2.txt, work for still_open.txt = 6.
	if dirtyBefore != 6 {
		t.Fatalf("expected 6 dirty backing files tracked before barrier, got %d", dirtyBefore)
	}

	// 2. Acquire checkpoint barrier: flushes 1 open handle + 5 closed dirty files + 3 backing dirs = 9 flushes.
	tok, err := core.BeginCheckpointBarrier(t.Context())
	testutil.FailErr(t, "BeginCheckpointBarrier 1", err)
	testutil.FailErr(t, "EndCheckpointBarrier 1", core.EndCheckpointBarrier(tok))

	afterFirstBarrier := core.Stats().Flushes
	if afterFirstBarrier != 9 {
		t.Fatalf("expected 9 flushes at first barrier (1 open + 5 closed dirty + 3 dirs), got %d", afterFirstBarrier)
	}

	core.mu.RLock()
	dirtyAfter := len(core.state.dirtyBackingFiles)
	core.mu.RUnlock()
	if dirtyAfter != 0 {
		t.Fatalf("expected dirtyBackingFiles cleared after barrier, got %d", dirtyAfter)
	}

	// 3. Second barrier without new writes flushes only the 1 still-open handle + 3 backing dirs = 4 flushes.
	tok2, err := core.BeginCheckpointBarrier(t.Context())
	testutil.FailErr(t, "BeginCheckpointBarrier 2", err)
	testutil.FailErr(t, "EndCheckpointBarrier 2", core.EndCheckpointBarrier(tok2))

	if delta := core.Stats().Flushes - afterFirstBarrier; delta != 4 {
		t.Fatalf("expected 4 flushes on second barrier (1 open handle + 3 dirs), got %d", delta)
	}
}
