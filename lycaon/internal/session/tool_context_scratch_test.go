package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type scratchFixture struct {
	mgr    *Host
	store  *store.Memory
	root   *api.Session
	worker *api.Session
	dir    func(sessionID string) string
}

func newScratchFixture(t *testing.T) scratchFixture {
	t.Helper()
	ctx := t.Context()
	stateRoot := t.TempDir()
	sessions := store.NewMemory()
	mgr := NewHost(sessions, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.SetScratchFolders(scratch.New(stateRoot))
	root, err := sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create root session", err)
	worker, err := sessions.CreateChild(ctx, root, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create worker session", err)
	canonicalRoot := fspath.CanonicalPath(stateRoot)
	return scratchFixture{
		mgr: mgr, store: sessions, root: root, worker: worker,
		dir: func(id string) string { return filepath.Join(canonicalRoot, "scratch", id) },
	}
}

func (f scratchFixture) toolContext(t *testing.T, sess *api.Session) tools.ToolContext {
	t.Helper()
	tctx, err := f.mgr.ToolContext.Build(t.Context(), sess, tools.DefaultToolProfileID, inject.Machine{})
	testutil.FailErr(t, "build tool context", err)
	return tctx
}

func TestEverySessionGetsItsOwnScratchFolder(t *testing.T) {
	f := newScratchFixture(t)

	rootCtx := f.toolContext(t, f.root)
	workerCtx := f.toolContext(t, f.worker)
	if rootCtx.SessionScratchDir != f.dir(f.root.ID) {
		t.Fatalf("coordinator scratch = %q, want %q", rootCtx.SessionScratchDir, f.dir(f.root.ID))
	}
	if workerCtx.SessionScratchDir != f.dir(f.worker.ID) {
		t.Fatalf("worker scratch = %q, want its own sibling folder %q", workerCtx.SessionScratchDir, f.dir(f.worker.ID))
	}
	for _, dir := range []string{rootCtx.SessionScratchDir, workerCtx.SessionScratchDir} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("scratch folder %q was not prepared: %v", dir, err)
		}
	}
}

func TestScratchUnavailableLeavesContextWithoutScratch(t *testing.T) {
	f := newScratchFixture(t)
	scratchRoot := filepath.Dir(f.dir(f.root.ID))
	testutil.FailErr(t, "plant link", os.Symlink(t.TempDir(), scratchRoot))

	if dir := f.toolContext(t, f.root).SessionScratchDir; dir != "" {
		t.Fatalf("scratch prepared through a linked root: %q", dir)
	}
}

func TestDeletingAChatRemovesScratchForItsWholeTree(t *testing.T) {
	f := newScratchFixture(t)
	f.toolContext(t, f.root)
	f.toolContext(t, f.worker)
	other, err := f.store.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create unrelated session", err)
	f.toolContext(t, other)

	testutil.FailErr(t, "delete chat", f.mgr.Chats.Delete(t.Context(), f.root.ID))
	for _, id := range []string{f.root.ID, f.worker.ID} {
		if _, err := os.Stat(f.dir(id)); !os.IsNotExist(err) {
			t.Fatalf("scratch for %q survived its chat's deletion: %v", id, err)
		}
	}
	if _, err := os.Stat(f.dir(other.ID)); err != nil {
		t.Fatalf("unrelated chat's scratch was removed: %v", err)
	}
}

func TestReclaimScratchKeepsChatsWithATurnInFlight(t *testing.T) {
	f := newScratchFixture(t)
	f.toolContext(t, f.root)
	f.toolContext(t, f.worker)
	busyFile := filepath.Join(f.dir(f.worker.ID), "draft.md")
	testutil.FailErr(t, "write worker scratch", os.WriteFile(busyFile, []byte("x"), 0o600))

	turn := f.mgr.Runner.Execution.Prompt.Acquire(f.worker.ID)
	turn.Lock()
	err := f.mgr.Runner.Execution.ReclaimScratch(t.Context())
	turn.Unlock()
	testutil.FailErr(t, "reclaim scratch", err)

	if _, err := os.Stat(f.dir(f.root.ID)); !os.IsNotExist(err) {
		t.Fatalf("idle chat's scratch survived reclaim: %v", err)
	}
	if _, err := os.Stat(busyFile); err != nil {
		t.Fatalf("scratch of a session mid-turn was removed: %v", err)
	}
}
