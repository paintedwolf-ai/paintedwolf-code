package native

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/pkg/api"
)

type agentFeedCap struct {
	mu    sync.Mutex
	evs   []api.SourceChange
	kinds []api.SourceWorkspaceKind
}

func (h *agentFeedCap) SourceChanged(_ context.Context, ev api.SourceChangesEvent) error {
	h.mu.Lock()
	h.evs = append(h.evs, ev.Changes...)
	h.kinds = append(h.kinds, ev.WorkspaceKind)
	h.mu.Unlock()
	return nil
}

func (h *agentFeedCap) lastKind() api.SourceWorkspaceKind {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.kinds) == 0 {
		return ""
	}
	return h.kinds[len(h.kinds)-1]
}

func (h *agentFeedCap) SourceChangedTx(ctx context.Context, _ *sql.Tx, ev api.SourceChangesEvent) error {
	return h.SourceChanged(ctx, ev)
}

func (h *agentFeedCap) Deliver() {}

func (h *agentFeedCap) take() []api.SourceChange {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := append([]api.SourceChange(nil), h.evs...)
	h.evs = nil
	return out
}

func TestAgentWriteAndEditEmitSourceChanged(t *testing.T) {
	hub := &agentFeedCap{}
	t.Cleanup(sourcefeed.Bind(hub))

	dir := t.TempDir()
	tctx := nativefixture.Context(dir)
	tctx.ProjectID = "proj-agent"
	tctx.UserTurn = 4

	wt := &WriteTool{Boundary: nativefixture.Boundary(t)}
	_, err := wt.Run(context.Background(), map[string]any{
		"path": "agent.go", "content": "package agent\n",
	}, tctx)
	testutil.FailErr(t, "write", err)
	got := hub.take()
	if len(got) != 1 || got[0].Origin != api.SourceChangeOriginAgent || got[0].Op != api.SourceChangeOpCreate || got[0].Turn != 4 {
		t.Fatalf("write emit: %+v", got)
	}
	if hub.lastKind() != api.SourceWorkspaceKindProject {
		t.Fatalf("write workspace kind = %q, want project", hub.lastKind())
	}

	et := &EditTool{Boundary: nativefixture.Boundary(t)}
	_, err = et.Run(context.Background(), map[string]any{
		"path": "agent.go", "old_string": "package agent\n", "new_string": "package edited\n",
	}, tctx)
	testutil.FailErr(t, "edit", err)
	got = hub.take()
	if len(got) != 1 || got[0].Origin != api.SourceChangeOriginAgent || got[0].Op != api.SourceChangeOpWrite {
		t.Fatalf("edit emit: %+v", got)
	}
}

func TestAgentMutationUsesToolContextWorkspace(t *testing.T) {
	root := t.TempDir()
	tctx := nativefixture.Context(root)
	tctx.ProjectID = "proj-worker"
	tctx.WorkerJobID = "job-1"
	tctx.SourceWorkspaceKind = api.SourceWorkspaceKindWorker
	_, change, ok := agentCommitInputs(tctx, agentMutation{
		Op: api.SourceChangeOpCreate, AbsPath: filepath.Join(root, "worker.go"),
	})
	if !ok {
		t.Fatal("agent mutation did not resolve")
	}
	if change.JobID != "job-1" || change.WorkspaceKind != api.SourceWorkspaceKindWorker {
		t.Fatalf("worker change = %+v", change)
	}
}

func TestAgentMkdirEmitsSourceChanged(t *testing.T) {
	hub := &agentFeedCap{}
	t.Cleanup(sourcefeed.Bind(hub))

	dir := t.TempDir()
	tctx := nativefixture.Context(dir)
	tctx.ProjectID = "proj-agent-mkdir"
	_, err := (&MkdirTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
		"paths": []any{"generated/nested"},
	}, tctx)
	testutil.FailErr(t, "mkdir", err)

	got := hub.take()
	if len(got) != 1 || got[0].Origin != api.SourceChangeOriginAgent ||
		got[0].Op != api.SourceChangeOpCreate || got[0].Path != "generated/nested" ||
		got[0].IsDir == nil || !*got[0].IsDir {
		t.Fatalf("mkdir emit: %+v", got)
	}
}

func TestAgentUTF16EditEmitsRawEncodedSHA(t *testing.T) {
	hub := &agentFeedCap{}
	t.Cleanup(sourcefeed.Bind(hub))

	dir := t.TempDir()
	path := filepath.Join(dir, "wide.txt")
	before := testutil.EncodeTextFixture(t, "before\n", textfile.UTF16BEBOM)
	testutil.FailErr(t, "write fixture", os.WriteFile(path, before, 0o644))
	tctx := nativefixture.Context(dir)
	tctx.ProjectID = "proj-agent-utf16"

	_, err := (&EditTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
		"path": "wide.txt", "old_string": "before", "new_string": "after",
	}, tctx)
	testutil.FailErr(t, "edit UTF-16", err)
	want := testutil.EncodeTextFixture(t, "after\n", textfile.UTF16BEBOM)
	gotRaw, err := os.ReadFile(path)
	testutil.FailErr(t, "read edited file", err)
	if !bytes.Equal(gotRaw, want) {
		t.Fatalf("edited raw bytes = %x, want %x", gotRaw, want)
	}
	events := hub.take()
	if len(events) != 1 || events[0].AfterSHA256 != textfile.SHA256(want) {
		t.Fatalf("source_changed events = %+v, want raw sha %s", events, textfile.SHA256(want))
	}
}
