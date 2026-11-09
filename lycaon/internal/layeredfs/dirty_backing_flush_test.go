package layeredfs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestDirtyBackingFlushRetainsFailedFileAndRetiresSucceeded(t *testing.T) {
	tmp := t.TempDir()
	trunk := filepath.Join(tmp, "trunk")
	backing := filepath.Join(tmp, "backing")
	testutil.FailErr(t, "mkdir trunk", os.MkdirAll(trunk, 0o755))
	testutil.FailErr(t, "mkdir backing", os.MkdirAll(backing, 0o700))

	var failTarget string
	core, err := New(Config{
		ViewID:     "view-ruling8-partial-fail",
		BackingDir: backing,
		Roots:      []RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true}},
		FaultHooks: &FaultHooks{
			JournalSyncHook: func(phase string) error {
				if failTarget != "" && phase == "dirty_backing:"+failTarget {
					return errors.New("injected dirty backing sync failure")
				}
				return nil
			},
		},
	})
	testutil.FailErr(t, "New Core", err)
	t.Cleanup(func() { _ = core.ReleaseView() })

	q := func(rel string) QualifiedPath { return QualifiedPath{RootID: "primary", Rel: rel} }
	_, err = core.WriteFile(q("alpha.txt"), []byte("alpha\n"), 0o644)
	testutil.FailErr(t, "WriteFile alpha.txt", err)
	_, err = core.WriteFile(q("beta.txt"), []byte("beta\n"), 0o644)
	testutil.FailErr(t, "WriteFile beta.txt", err)

	core.mu.RLock()
	alphaNode := core.state.nodesByID[core.state.pathIndex[q("alpha.txt")]]
	betaNode := core.state.nodesByID[core.state.pathIndex[q("beta.txt")]]
	alphaBacking := alphaNode.flags.backingPath
	betaBacking := betaNode.flags.backingPath
	core.mu.RUnlock()

	failTarget = betaBacking
	if err := flushBackingState(core); err == nil {
		t.Fatalf("expected flushBackingState to return injected error for %s", betaBacking)
	}

	core.mu.RLock()
	_, alphaStillDirty := core.state.dirtyBackingFiles[alphaBacking]
	_, betaStillDirty := core.state.dirtyBackingFiles[betaBacking]
	core.mu.RUnlock()
	if alphaStillDirty {
		t.Fatalf("expected succeeded file %s to be retired from dirtyBackingFiles", alphaBacking)
	}
	if !betaStillDirty {
		t.Fatalf("expected failed file %s to remain in dirtyBackingFiles", betaBacking)
	}

	// Clearing the fault and flushing again retires the remaining failed file.
	failTarget = ""
	testutil.FailErr(t, "retry flushBackingState", flushBackingState(core))
	core.mu.RLock()
	remaining := len(core.state.dirtyBackingFiles)
	core.mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("expected 0 remaining dirtyBackingFiles after retry, got %d", remaining)
	}
}

func TestDirtyBackingFlushPreservesNewerGenerationDuringInFlightFlush(t *testing.T) {
	tmp := t.TempDir()
	trunk := filepath.Join(tmp, "trunk")
	backing := filepath.Join(tmp, "backing")
	testutil.FailErr(t, "mkdir trunk", os.MkdirAll(trunk, 0o755))
	testutil.FailErr(t, "mkdir backing", os.MkdirAll(backing, 0o700))

	var core *Core
	var targetBacking string
	var bumpedGen uint64
	core, err := New(Config{
		ViewID:     "view-ruling8-generation-race",
		BackingDir: backing,
		Roots:      []RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true}},
		FaultHooks: &FaultHooks{
			JournalSyncHook: func(phase string) error {
				if targetBacking != "" && strings.HasPrefix(phase, "dirty_backing:") && phase == "dirty_backing:"+targetBacking {
					// Simulate a concurrent write dirtying the same backing file with a newer generation
					// while flushBackingState is in flight outside c.mu.
					core.mu.Lock()
					markDirtyBackingFileLocked(core, targetBacking)
					bumpedGen = core.state.dirtyBackingFiles[targetBacking]
					core.mu.Unlock()
				}
				return nil
			},
		},
	})
	testutil.FailErr(t, "New Core", err)
	t.Cleanup(func() { _ = core.ReleaseView() })

	q := func(rel string) QualifiedPath { return QualifiedPath{RootID: "primary", Rel: rel} }
	_, err = core.WriteFile(q("concurrent.txt"), []byte("v1\n"), 0o644)
	testutil.FailErr(t, "WriteFile concurrent.txt", err)

	core.mu.RLock()
	node := core.state.nodesByID[core.state.pathIndex[q("concurrent.txt")]]
	targetBacking = node.flags.backingPath
	initialGen := core.state.dirtyBackingFiles[targetBacking]
	core.mu.RUnlock()

	testutil.FailErr(t, "flushBackingState", flushBackingState(core))
	if bumpedGen <= initialGen {
		t.Fatalf("expected bumped generation > initial generation (%d <= %d)", bumpedGen, initialGen)
	}

	core.mu.RLock()
	gotGen, stillDirty := core.state.dirtyBackingFiles[targetBacking]
	core.mu.RUnlock()
	if !stillDirty || gotGen != bumpedGen {
		t.Fatalf("expected newer generation %d to be preserved in dirtyBackingFiles, got stillDirty=%v gen=%d",
			bumpedGen, stillDirty, gotGen)
	}
}
