package sourcefeed

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type capturePub struct {
	mu         sync.Mutex
	evs        []api.SourceChangesEvent
	err        error
	deliveries atomic.Int32
}

func (c *capturePub) SourceChanged(_ context.Context, ev api.SourceChangesEvent) error {
	if c.err != nil {
		return c.err
	}
	c.mu.Lock()
	c.evs = append(c.evs, ev)
	c.mu.Unlock()
	return nil
}

func (c *capturePub) SourceChangedTx(ctx context.Context, _ *sql.Tx, ev api.SourceChangesEvent) error {
	return c.SourceChanged(ctx, ev)
}

func (c *capturePub) Deliver() { c.deliveries.Add(1) }

func (c *capturePub) events() []api.SourceChange {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []api.SourceChange
	for _, event := range c.evs {
		out = append(out, event.Changes...)
	}
	return out
}

func (c *capturePub) batches() []api.SourceChangesEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]api.SourceChangesEvent(nil), c.evs...)
}

func TestEmitPublishesSourceChanged(t *testing.T) {
	cap := &capturePub{}
	t.Cleanup(Bind(cap))

	testutil.FailErr(t, "emit", Emit(context.Background(), Change{
		ProjectID:     "proj-1",
		WorkspaceID:   "ws-1",
		WorkspaceKind: api.SourceWorkspaceKindProject,
		RootID:        "r1",
		Path:          "src/a.go",
		Op:            api.SourceChangeOpWrite,
		Origin:        api.SourceChangeOriginUser,
		AfterSHA256:   "abc",
	}))
	got := cap.events()
	if len(got) != 1 {
		t.Fatalf("events=%d want 1", len(got))
	}
	if got[0].Path != "src/a.go" || got[0].Op != api.SourceChangeOpWrite || got[0].Origin != api.SourceChangeOriginUser {
		t.Fatalf("unexpected event: %+v", got[0])
	}
	if batches := cap.batches(); len(batches) != 1 || batches[0].WorkspaceKind != api.SourceWorkspaceKindProject {
		t.Fatalf("workspace kind = %+v, want project", batches)
	}
}

func TestSourceMetadataRenameDoesNotReadMovedContents(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "large")
	file, err := os.Create(path)
	testutil.FailErr(t, "create sparse source", err)
	testutil.FailErr(t, "size source", file.Truncate(3<<30))
	testutil.FailErr(t, "close source", file.Close())
	prepared, err := prepareChanges([]Change{{ProjectID: "project", WorkspaceID: "workspace", WorkspaceKind: api.SourceWorkspaceKindProject, RootID: "root", Path: "large", FromPath: "old", AbsPath: path, Op: api.SourceChangeOpRename, Origin: api.SourceChangeOriginUser}}, false)
	testutil.FailErr(t, "prepare rename event", err)
	if len(prepared.writePaths) != 0 || len(prepared.event.Changes) != 1 {
		t.Fatalf("metadata move scheduled content observation: %+v", prepared)
	}
}

func TestEmitRenameCarriesFromPath(t *testing.T) {
	cap := &capturePub{}
	t.Cleanup(Bind(cap))

	testutil.FailErr(t, "emit rename", Emit(context.Background(), Change{
		ProjectID:     "proj-1",
		WorkspaceID:   "ws-1",
		WorkspaceKind: api.SourceWorkspaceKindProject,
		RootID:        "r1",
		Path:          "b.go",
		FromPath:      "a.go",
		Op:            api.SourceChangeOpRename,
		Origin:        api.SourceChangeOriginUser,
	}))
	got := cap.events()
	if len(got) != 1 || got[0].FromPath != "a.go" || got[0].Path != "b.go" {
		t.Fatalf("rename shape: %+v", got)
	}
}

func TestEmitRejectsMalformedChange(t *testing.T) {
	cap := &capturePub{}
	t.Cleanup(Bind(cap))

	err := Emit(t.Context(), Change{
		ProjectID: "proj-1", WorkspaceID: "ws-1", RootID: "r1", Path: "a.go",
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
	})
	if err == nil {
		t.Fatal("emit accepted a missing workspace kind")
	}
	if len(cap.batches()) != 0 {
		t.Fatalf("malformed change was published: %+v", cap.batches())
	}
}

func TestEmitBatchRejectsMixedWorkspaceKinds(t *testing.T) {
	cap := &capturePub{}
	t.Cleanup(Bind(cap))
	target := filepath.Join(t.TempDir(), "a.go")

	err := EmitBatch(t.Context(), []Change{
		{ProjectID: "proj-1", WorkspaceID: "ws-1", WorkspaceKind: api.SourceWorkspaceKindProject, RootID: "r1", Path: "a.go", Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent, AbsPath: target},
		{ProjectID: "proj-1", WorkspaceID: "ws-1", WorkspaceKind: api.SourceWorkspaceKindWorker, RootID: "r1", Path: "b.go", Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent},
	})
	if err == nil {
		t.Fatal("emit accepted a mixed workspace batch")
	}
	if len(cap.batches()) != 0 {
		t.Fatalf("mixed workspace batch was published: %+v", cap.batches())
	}
	if isRecentHostWrite(target) {
		t.Fatal("rejected batch changed watcher suppression state")
	}
}

func TestFailedEmitDoesNotSuppressWatcherFallback(t *testing.T) {
	target := filepath.Join(t.TempDir(), "failed.go")
	cap := &capturePub{err: fmt.Errorf("enqueue failed")}
	t.Cleanup(Bind(cap))
	err := Emit(t.Context(), Change{
		ProjectID: "proj-1", WorkspaceID: "ws-1", WorkspaceKind: api.SourceWorkspaceKindProject,
		RootID: "r1", Path: "failed.go", Op: api.SourceChangeOpWrite,
		Origin: api.SourceChangeOriginAgent, AbsPath: target,
	})
	if err == nil {
		t.Fatal("failed enqueue returned no error")
	}
	if isRecentHostWrite(target) {
		t.Fatal("failed enqueue suppressed watcher fallback")
	}
}

func TestTransactionalEmitSuppressesWatcherOnlyAfterCommit(t *testing.T) {
	target := filepath.Join(t.TempDir(), "committed.go")
	change := Change{
		ProjectID: "proj-1", WorkspaceID: "ws-1", WorkspaceKind: api.SourceWorkspaceKindProject,
		RootID: "r1", Path: "committed.go", Op: api.SourceChangeOpWrite,
		Origin: api.SourceChangeOriginAgent, AbsPath: target,
	}
	cap := &capturePub{}
	t.Cleanup(Bind(cap))
	delivery, err := EmitTx(t.Context(), nil, change)
	testutil.FailErr(t, "stage event", err)
	if isRecentHostWrite(target) {
		t.Fatal("uncommitted event suppressed watcher fallback")
	}
	change.AbsPath = filepath.Join(t.TempDir(), "mutated.go")
	delivery.DeliverCommitted()
	delivery.DeliverCommitted()
	if !isRecentHostWrite(target) {
		t.Fatal("committed event did not suppress watcher echo")
	}
	if isRecentHostWrite(change.AbsPath) {
		t.Fatal("delivery used mutable caller state")
	}
	if cap.deliveries.Load() != 1 {
		t.Fatalf("delivery count=%d want 1", cap.deliveries.Load())
	}
}

func TestEmitBatchPublishesOneWorkspaceEvent(t *testing.T) {
	cap := &capturePub{}
	t.Cleanup(Bind(cap))
	changes := []Change{
		{ProjectID: "proj-1", WorkspaceID: "ws-1", WorkspaceKind: api.SourceWorkspaceKindProject, RootID: "r1", Path: "a.go", Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent},
		{ProjectID: "proj-1", WorkspaceID: "ws-1", WorkspaceKind: api.SourceWorkspaceKindProject, RootID: "r1", Path: "b.go", Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent},
	}
	testutil.FailErr(t, "emit batch", EmitBatch(t.Context(), changes))
	batches := cap.batches()
	if len(batches) != 1 || len(batches[0].Changes) != 2 || batches[0].WorkspaceID != "ws-1" {
		t.Fatalf("batches = %+v, want one two-change workspace batch", batches)
	}
}

func TestEmitBatchBoundsOverflowAndRequestsResync(t *testing.T) {
	cap := &capturePub{}
	t.Cleanup(Bind(cap))
	changes := make([]Change, 513)
	for i := range changes {
		changes[i] = Change{
			ProjectID: "proj-1", WorkspaceID: "ws-1", RootID: "r1",
			WorkspaceKind: api.SourceWorkspaceKindProject,
			Path:          fmt.Sprintf("file-%03d.go", i), Op: api.SourceChangeOpWrite,
			Origin: api.SourceChangeOriginExternal,
		}
	}
	testutil.FailErr(t, "emit overflow batch", EmitBatch(t.Context(), changes))
	batches := cap.batches()
	if len(batches) != 1 || len(batches[0].Changes) != 512 || !batches[0].Resync {
		t.Fatalf("overflow batch = %+v", batches)
	}
}

func TestEmitBatchValidatesChangesBeyondWireLimit(t *testing.T) {
	cap := &capturePub{}
	t.Cleanup(Bind(cap))
	changes := make([]Change, maxChangesPerEvent+1)
	for i := range changes {
		changes[i] = Change{
			ProjectID: "proj-1", WorkspaceID: "ws-1", RootID: "r1",
			WorkspaceKind: api.SourceWorkspaceKindProject,
			Path:          fmt.Sprintf("file-%03d.go", i), Op: api.SourceChangeOpWrite,
			Origin: api.SourceChangeOriginAgent,
		}
	}
	changes[len(changes)-1].WorkspaceID = "ws-2"
	if err := EmitBatch(t.Context(), changes); err == nil {
		t.Fatal("emit accepted an invalid change beyond the wire limit")
	}
	if len(cap.batches()) != 0 {
		t.Fatalf("invalid overflow batch was published: %+v", cap.batches())
	}
}

func TestEmitBatchSuppressesAllCommittedWritesBeyondWireLimit(t *testing.T) {
	cap := &capturePub{}
	t.Cleanup(Bind(cap))
	dir := t.TempDir()
	changes := make([]Change, maxChangesPerEvent+1)
	for i := range changes {
		changes[i] = Change{
			ProjectID: "proj-1", WorkspaceID: "ws-1", RootID: "r1",
			WorkspaceKind: api.SourceWorkspaceKindProject,
			Path:          fmt.Sprintf("file-%03d.go", i), Op: api.SourceChangeOpWrite,
			Origin: api.SourceChangeOriginAgent, AbsPath: filepath.Join(dir, fmt.Sprintf("file-%03d.go", i)),
		}
	}
	testutil.FailErr(t, "emit overflow batch", EmitBatch(t.Context(), changes))
	if !isRecentHostWrite(changes[len(changes)-1].AbsPath) {
		t.Fatal("overflow write was not suppressed")
	}
}

func TestPublishCleanupOnlyClearsMatchingBinding(t *testing.T) {
	first := &capturePub{}
	second := &capturePub{}
	unbindFirst := Bind(first)
	unbindSecond := Bind(second)
	t.Cleanup(unbindSecond)

	unbindFirst()
	Emit(context.Background(), Change{
		ProjectID: "p1", WorkspaceID: "ws-1", RootID: "r1", Path: "a.go",
		WorkspaceKind: api.SourceWorkspaceKindProject,
		Op:            api.SourceChangeOpWrite, Origin: api.SourceChangeOriginUser,
	})
	if len(first.events()) != 0 || len(second.events()) != 1 {
		t.Fatalf("older cleanup changed successor binding: first=%d second=%d", len(first.events()), len(second.events()))
	}

	unbindSecond()
	Emit(context.Background(), Change{
		ProjectID: "p1", WorkspaceID: "ws-1", RootID: "r1", Path: "b.go",
		WorkspaceKind: api.SourceWorkspaceKindProject,
		Op:            api.SourceChangeOpWrite, Origin: api.SourceChangeOriginUser,
	})
	if len(second.events()) != 1 {
		t.Fatalf("matching cleanup left publisher binding active: events=%d", len(second.events()))
	}
}

func TestWatcherExternalDebounce(t *testing.T) {
	cap := &capturePub{}
	t.Cleanup(Bind(cap))

	dir := t.TempDir()
	projectID := "proj-watch"
	var ledgerCalls atomic.Int32
	EnsureProjectWatch(t.Context(), projectID, "", []RootSpec{{ID: "r1", WorkspaceID: "ws-watch", Path: dir}}, func(
		context.Context,
		string,
		ExternalBatch,
	) {
		ledgerCalls.Add(1)
	})
	t.Cleanup(func() { StopProjectWatch(t.Context(), projectID) })

	target := filepath.Join(dir, "ext.txt")
	testutil.FailErr(t, "write external", os.WriteFile(target, []byte("one"), 0o644))
	testutil.FailErr(t, "write burst", os.WriteFile(target, []byte("two"), 0o644))
	testutil.FailErr(t, "write burst2", os.WriteFile(target, []byte("three"), 0o644))

	waitForWatcherBarrier(t, cap, "ext.txt")
	batches := cap.batches()
	if len(batches) != 1 {
		t.Fatalf("coalesced batches=%d want 1: %+v", len(batches), batches)
	}
	batch := batches[0]
	if batch.Resync {
		if len(batch.Changes) != 0 {
			t.Fatalf("resync carried partial file changes: %+v", batch)
		}
	} else if len(batch.Changes) != 1 || batch.Changes[0].Origin != api.SourceChangeOriginExternal || batch.Changes[0].Path != "ext.txt" {
		t.Fatalf("unexpected external changes: %+v", batch.Changes)
	}
	if got := ledgerCalls.Load(); got != 1 {
		t.Fatalf("external ledger call count=%d want 1", got)
	}
}

func TestWatcherSkipsGit(t *testing.T) {
	cap := &capturePub{}
	t.Cleanup(Bind(cap))

	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	testutil.FailErr(t, "mkdir .git", os.MkdirAll(gitDir, 0o755))
	projectID := "proj-git"
	EnsureProjectWatch(t.Context(), projectID, "", []RootSpec{{ID: "r1", WorkspaceID: "ws-git", Path: dir}}, nil)
	t.Cleanup(func() { StopProjectWatch(t.Context(), projectID) })

	testutil.FailErr(t, "write .git", os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref"), 0o644))
	testutil.FailErr(t, "write watcher barrier", os.WriteFile(filepath.Join(dir, "barrier.txt"), []byte("done"), 0o644))
	waitForWatcherBarrier(t, cap, "barrier.txt")
	for _, event := range cap.events() {
		if event.Path != "barrier.txt" {
			t.Fatalf(".git should be silent, got %+v", event)
		}
	}
}

func TestSelfWriteDrop(t *testing.T) {
	cap := &capturePub{}
	t.Cleanup(Bind(cap))

	dir := t.TempDir()
	projectID := "proj-self"
	EnsureProjectWatch(t.Context(), projectID, "", []RootSpec{{ID: "r1", WorkspaceID: "ws-self", Path: dir}}, nil)
	t.Cleanup(func() { StopProjectWatch(t.Context(), projectID) })

	target := filepath.Join(dir, "host.txt")
	testutil.FailErr(t, "host write", os.WriteFile(target, []byte("host"), 0o644))
	Emit(context.Background(), Change{
		ProjectID:     projectID,
		WorkspaceID:   "ws-self",
		WorkspaceKind: api.SourceWorkspaceKindProject,
		RootID:        "r1",
		Path:          "host.txt",
		Op:            api.SourceChangeOpWrite,
		Origin:        api.SourceChangeOriginUser,
		AbsPath:       target,
	})
	testutil.FailErr(t, "echo write", os.WriteFile(target, []byte("host"), 0o644))
	testutil.FailErr(t, "write watcher barrier", os.WriteFile(filepath.Join(dir, "barrier.txt"), []byte("done"), 0o644))
	resynced := waitForWatcherBarrier(t, cap, "barrier.txt")
	got := cap.events()
	if len(got) != 2 && !(resynced && len(got) == 1) {
		t.Fatalf("self-write drop want host event plus watcher acknowledgement, got %d: %+v", len(got), got)
	}
	if got[0].Origin != api.SourceChangeOriginUser {
		t.Fatalf("want user origin, got %+v", got[0])
	}
	for _, event := range got {
		if event.Path == "host.txt" && event.Origin == api.SourceChangeOriginExternal {
			t.Fatalf("self-write echoed as external event: %+v", got)
		}
	}
}

func TestHostWriteSuppressionPrunesExpiredPaths(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "stale.go")
	current := filepath.Join(dir, "current.go")
	selfMu.Lock()
	selfWrite[stale] = hostWriteReceipt{at: time.Now().Add(-2 * selfWriteWindow)}
	selfSweepAt = time.Time{}
	selfMu.Unlock()

	NoteHostWrite(current)
	selfMu.Lock()
	_, staleFound := selfWrite[stale]
	_, currentFound := selfWrite[current]
	delete(selfWrite, current)
	selfMu.Unlock()
	if staleFound || !currentFound {
		t.Fatalf("suppression entries stale=%v current=%v", staleFound, currentFound)
	}
}

func TestHostEmitContinuesWhileWatcherIsActive(t *testing.T) {
	cap := &capturePub{}
	t.Cleanup(Bind(cap))

	projectID := "proj-deg"
	dir := t.TempDir()
	EnsureProjectWatch(t.Context(), projectID, "", []RootSpec{{ID: "r1", WorkspaceID: "ws-deg", Path: dir}}, nil)
	t.Cleanup(func() { StopProjectWatch(t.Context(), projectID) })
	Emit(context.Background(), Change{
		ProjectID:     projectID,
		WorkspaceID:   "ws-deg",
		WorkspaceKind: api.SourceWorkspaceKindProject,
		RootID:        "r1",
		Path:          "still.go",
		Op:            api.SourceChangeOpWrite,
		Origin:        api.SourceChangeOriginAgent,
	})
	if len(cap.events()) != 1 {
		t.Fatalf("host emits must continue")
	}
}

// A resync acknowledges a watcher batch without naming individual paths.
func waitForWatcherBarrier(t *testing.T, cap *capturePub, path string) bool {
	t.Helper()
	resynced := false
	ready := testutil.WaitForNoFatal(2*time.Second, func() bool {
		for _, batch := range cap.batches() {
			if batch.Resync {
				resynced = true
				return true
			}
		}
		for _, event := range cap.events() {
			if event.Path == path {
				return true
			}
		}
		return false
	})
	if !ready {
		t.Fatalf("watcher did not acknowledge %q: batches=%+v", path, cap.batches())
	}
	return resynced
}
