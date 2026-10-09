package worker_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

type promoteLedgerStub struct {
	mu         sync.Mutex
	records    []sourceledger.RecordInput
	firstWrite []string
}

func (s *promoteLedgerStub) Record(_ context.Context, in sourceledger.RecordInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, in)
	return nil
}

type captureFeed struct {
	mu    sync.Mutex
	evs   []api.SourceChange
	kinds []api.SourceWorkspaceKind
}

func (c *captureFeed) SourceChanged(_ context.Context, ev api.SourceChangesEvent) error {
	c.mu.Lock()
	c.evs = append(c.evs, ev.Changes...)
	c.kinds = append(c.kinds, ev.WorkspaceKind)
	c.mu.Unlock()
	return nil
}

func (c *captureFeed) SourceChangedTx(ctx context.Context, _ *sql.Tx, ev api.SourceChangesEvent) error {
	return c.SourceChanged(ctx, ev)
}

func (c *captureFeed) Deliver() {}

func (c *captureFeed) events() []api.SourceChange {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]api.SourceChange(nil), c.evs...)
}

func (c *captureFeed) workspaceKinds() []api.SourceWorkspaceKind {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]api.SourceWorkspaceKind(nil), c.kinds...)
}

func (s *promoteLedgerStub) RecordTx(ctx context.Context, _ *sql.Tx, in sourceledger.RecordInput) error {
	return s.Record(ctx, in)
}

func (s *promoteLedgerStub) RecordBatch(ctx context.Context, inputs []sourceledger.RecordInput) error {
	for _, input := range inputs {
		if err := s.Record(ctx, input); err != nil {
			return err
		}
	}
	return nil
}

func (s *promoteLedgerStub) RecordBatchTx(ctx context.Context, _ *sql.Tx, inputs []sourceledger.RecordInput) error {
	return s.RecordBatch(ctx, inputs)
}

func (s *promoteLedgerStub) JobVersionForPath(context.Context, string, string, string, string) (string, string, error) {
	return "", "", nil
}

func (s *promoteLedgerStub) JobPathFirstWriteOrder(context.Context, string, string) ([]string, error) {
	return append([]string(nil), s.firstWrite...), nil
}

func TestPromoteOverlayEmitsRootScopeSourceChanges(t *testing.T) {
	ctx := context.Background()
	primary := t.TempDir()
	testutil.FailErr(t, "MkdirAll primary", os.MkdirAll(filepath.Join(primary, "src"), 0o755))
	testutil.FailErr(t, "WriteFile base", os.WriteFile(filepath.Join(primary, "src", "edited.go"), []byte("base\n"), 0o644))
	const rootID = "root-id"
	manager := workspace.NewManager(t.TempDir(), t.TempDir())
	roots := []projectroot.RootRef{{
		ID: rootID, Label: "project", Path: primary, IsPrimary: true,
	}}
	binding, _, err := manager.CreateWorkerWorkspaceFromSources(ctx, roots, roots, rootID, "job-feed")
	testutil.FailErr(t, "create worker workspace", err)
	overlay := binding.Root
	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"src/**"}}
	baseline := workspaceBaselinePath(t, overlay)
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(filepath.Join(overlay, "src"), 0o755))
	testutil.FailErr(t, "WriteFile new", os.WriteFile(filepath.Join(overlay, "src", "added.go"), []byte("added\n"), 0o644))
	testutil.FailErr(t, "WriteFile edit", os.WriteFile(filepath.Join(overlay, "src", "edited.go"), []byte("edited\n"), 0o644))

	testutil.FailErr(t, "write discarded file", os.WriteFile(filepath.Join(overlay, "src", "discarded.go"), []byte("discarded\n"), 0o644))

	feed := &captureFeed{}
	t.Cleanup(sourcefeed.Bind(feed))

	task := &api.WorkerTask{
		ID:                    "job-feed",
		ParentSessionID:       "parent-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         primary,
		WorkspaceRoot:         overlay,
		WorkspaceRootID:       rootID,
		WorkspaceBaselinePath: baseline,
		MergeStatus:           api.WorkerMergeStatusPending,
		AgentType:             "implementer",
		Scope:                 &scope,
		Status:                api.WorkerStatusComplete,
	}
	ledger := &promoteLedgerStub{
		// Promotion preserves first-write order.
		firstWrite: []string{"src/edited.go", "src/added.go"},
	}
	svc := &worker.MergeService{
		Queue: &mergeQueueStub{task: task},

		Store:        &mergeStoreStub{},
		Reject:       mergeRejectFmt(t),
		SourceLedger: ledger, SourceHistory: ledger,
	}

	out, err := svc.PromoteOverlay(ctx, "parent-1", "job-feed", api.PromoteOverlayInput{
		ToolCallID: "call-9",
		UserTurn:   4,
		Resolutions: []api.WorkerPromoteResolution{
			{Path: "src/edited.go", Content: "resolved\n"},
			{Path: "src/discarded.go", Action: api.WorkerPromoteResolutionActionDrop},
		},
	})
	testutil.FailErr(t, "PromoteOverlay", err)
	if out.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("status=%q want merged", out.Status)
	}
	assertPromotionSnapshots(t, out)
	for _, kind := range feed.workspaceKinds() {
		if kind != api.SourceWorkspaceKindProject {
			t.Fatalf("promoted workspace kind = %q, want project", kind)
		}
	}

	byPath := map[string][]api.SourceChange{}
	for _, ev := range feed.events() {
		byPath[ev.Path] = append(byPath[ev.Path], ev)
	}
	var paths []string
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	want := []string{"src/added.go", "src/edited.go"}
	if len(paths) != len(want) || paths[0] != want[0] || paths[1] != want[1] {
		t.Fatalf("emitted paths = %v, want %v", paths, want)
	}
	for _, p := range want {
		evs := byPath[p]
		if len(evs) != 1 {
			t.Fatalf("%s emitted %d events, want exactly 1 per landed path", p, len(evs))
		}
		ev := evs[0]
		if ev.WorkerID != "job-feed" {
			t.Fatalf("%s job_id=%q want job-feed", p, ev.WorkerID)
		}
		if ev.Origin != api.SourceChangeOriginAgent {
			t.Fatalf("%s origin=%q want agent", p, ev.Origin)
		}
		if ev.SessionID != "parent-1" {
			t.Fatalf("%s session_id=%q want the coordinator session", p, ev.SessionID)
		}
		if ev.ToolCallID != "call-9" {
			t.Fatalf("%s tool_call_id=%q — promote provenance is how a landed row names its worker", p, ev.ToolCallID)
		}
		if ev.AfterSHA256 == "" {
			t.Fatalf("%s after_sha256 empty — the seen lens compares against it", p)
		}
		if ev.Turn != 4 {
			t.Fatalf("%s turn=%d want 4, the turn its ledger record carries", p, ev.Turn)
		}
	}
	if op := byPath["src/added.go"][0].Op; op != api.SourceChangeOpCreate {
		t.Fatalf("src/added.go op=%q want create", op)
	}
	if op := byPath["src/edited.go"][0].Op; op != api.SourceChangeOpWrite {
		t.Fatalf("src/edited.go op=%q want write", op)
	}

	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if len(ledger.records) != 2 {
		t.Fatalf("ledger records = %d, want 2", len(ledger.records))
	}
	if ledger.records[0].Path != "src/edited.go" || ledger.records[1].Path != "src/added.go" {
		t.Fatalf("record order = [%s %s], want worker first-write [src/edited.go src/added.go]",
			ledger.records[0].Path, ledger.records[1].Path)
	}
	tx := ledger.records[0].OperationID
	if tx == "" {
		t.Fatal("promote landing must share one operation_id")
	}
	for i, rec := range ledger.records {
		if rec.OperationID != tx {
			t.Fatalf("record[%d] operation_id=%q want shared %q", i, rec.OperationID, tx)
		}
		if rec.Cause != sourceledger.CauseOverlayPromote {
			t.Fatalf("record[%d] cause=%q want %q", i, rec.Cause, sourceledger.CauseOverlayPromote)
		}
		if rec.ToolCallID != "call-9" {
			t.Fatalf("record[%d] tool_call_id=%q want call-9", i, rec.ToolCallID)
		}
		if rec.ToolName != "promote_overlay" {
			t.Fatalf("record[%d] tool_name=%q want promote_overlay", i, rec.ToolName)
		}
		if rec.JobID != "job-feed" {
			t.Fatalf("record[%d] job_id=%q want job-feed", i, rec.JobID)
		}
	}
}

func assertPromotionSnapshots(t *testing.T, out api.WorkerMergeResult) {
	t.Helper()
	if out.OverlayPromotion == nil {
		t.Fatalf("missing promotion snapshots: %#v", out.OverlayPromotion)
	}
	files := out.OverlayPromotion.Files
	if len(files) != 2 {
		t.Fatalf("promotion files = %#v, want two landed files", files)
	}
	edited, added := files[0], files[1]
	if edited.RootID != "root-id" || edited.Path != "src/edited.go" || edited.Before == nil || *edited.Before != "base\n" || edited.After != "resolved\n" {
		t.Fatalf("edited snapshot = %#v, want primary-to-resolved bytes", edited)
	}
	if added.Path != "src/added.go" || added.Before != nil || added.After != "added\n" {
		t.Fatalf("new file snapshot = %#v", added)
	}
}

// mockDocumentSynchronizer records the hold lifecycle a promotion drives.
type mockDocumentSynchronizer struct {
	held      []editordoc.PathRef
	synced    []editordoc.PromotedDocumentSync
	res       editordoc.PromotedDocumentResult
	events    []string
	releases  []bool
	committed bool
	onRelease func()
}

func (m *mockDocumentSynchronizer) HoldPromotedDocuments(_ context.Context, _ *project.Project, refs []editordoc.PathRef) (editordoc.PromotedDocumentHold, error) {
	m.held = append(m.held, refs...)
	m.events = append(m.events, "hold")
	return m, nil
}

func (m *mockDocumentSynchronizer) Stage(_ context.Context, syncs []editordoc.PromotedDocumentSync) map[editordoc.PathRef]editordoc.PromotedDocumentResult {
	m.events = append(m.events, "stage")
	m.synced = append(m.synced, syncs...)
	out := make(map[editordoc.PathRef]editordoc.PromotedDocumentResult, len(syncs))
	for _, req := range syncs {
		out[editordoc.PathRef{RootID: req.RootID, Path: req.Path}] = m.res
	}
	return out
}

func (m *mockDocumentSynchronizer) CommitTx(context.Context, *sql.Tx) error {
	m.events = append(m.events, "commit")
	m.committed = true
	return nil
}

// Release follows the hold contract: only the first call has an effect.
func (m *mockDocumentSynchronizer) Release(_ context.Context, committed bool) {
	if len(m.releases) > 0 {
		return
	}
	m.events = append(m.events, "release")
	m.releases = append(m.releases, committed)
	if m.onRelease != nil {
		m.onRelease()
	}
}

type testProjectStore struct {
	p *project.Project
}

func (s *testProjectStore) Get(_ context.Context, _ string) (*project.Project, error) {
	return s.p, nil
}

type editorSyncPromotion struct {
	svc     *worker.MergeService
	primary string
	rootID  string
	ledger  *promoteLedgerStub
	docs    *mockDocumentSynchronizer
}

func newEditorSyncPromotion(t *testing.T, store *mergeStoreStub) editorSyncPromotion {
	t.Helper()
	ctx := context.Background()
	primary := t.TempDir()
	testutil.FailErr(t, "WriteFile base", os.WriteFile(filepath.Join(primary, "edited.go"), []byte("base\n"), 0o644))
	const rootID = "root-id"
	manager := workspace.NewManager(t.TempDir(), t.TempDir())
	roots := []projectroot.RootRef{{
		ID: rootID, Label: "project", Path: primary, IsPrimary: true,
	}}
	binding, _, err := manager.CreateWorkerWorkspaceFromSources(ctx, roots, roots, rootID, "job-sync")
	testutil.FailErr(t, "create worker workspace", err)
	overlay := binding.Root
	baseline := workspaceBaselinePath(t, overlay)
	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"edited.go"}}
	testutil.FailErr(t, "WriteFile edit", os.WriteFile(filepath.Join(overlay, "edited.go"), []byte("promoted\n"), 0o644))

	task := &api.WorkerTask{
		ID:                    "job-sync",
		ParentSessionID:       "parent-coord",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         primary,
		WorkspaceRoot:         overlay,
		WorkspaceRootID:       rootID,
		WorkspaceBaselinePath: baseline,
		MergeStatus:           api.WorkerMergeStatusPending,
		AgentType:             "implementer",
		Scope:                 &scope,
		Status:                api.WorkerStatusComplete,
	}

	ledger := &promoteLedgerStub{}
	syncStub := &mockDocumentSynchronizer{
		res: editordoc.PromotedDocumentResult{
			DocumentID: "doc-1",
			TextBefore: &sourceledger.TextState{DocumentID: "doc-1", Revision: 1},
			TextAfter:  &sourceledger.TextState{DocumentID: "doc-1", Revision: 2},
		},
	}
	projects := &testProjectStore{p: &project.Project{ID: testdbseed.DefaultProjectID}}
	svc := &worker.MergeService{
		Queue:        &mergeQueueStub{task: task},
		Store:        store,
		Reject:       mergeRejectFmt(t),
		SourceLedger: ledger, SourceHistory: ledger,
		Projects:  projects,
		Documents: syncStub,
	}
	return editorSyncPromotion{svc: svc, primary: primary, rootID: rootID, ledger: ledger, docs: syncStub}
}

func TestPromoteOverlaySynchronizesEditorDocuments(t *testing.T) {
	f := newEditorSyncPromotion(t, &mergeStoreStub{})
	rootID, ledger, syncStub := f.rootID, f.ledger, f.docs
	out, err := f.svc.PromoteOverlay(context.Background(), "parent-coord", "job-sync", api.PromoteOverlayInput{
		ToolCallID: "call-promote-1",
		UserTurn:   3,
	})
	testutil.FailErr(t, "PromoteOverlay", err)
	if out.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("status=%q want merged", out.Status)
	}

	if len(syncStub.synced) != 1 {
		t.Fatalf("expected 1 synced document, got %d", len(syncStub.synced))
	}
	if len(syncStub.held) != 1 || syncStub.held[0] != (editordoc.PathRef{RootID: rootID, Path: "edited.go"}) {
		t.Fatalf("held = %+v", syncStub.held)
	}
	if got := strings.Join(syncStub.events, ","); got != "hold,stage,commit,release" || !syncStub.releases[0] {
		t.Fatalf("hold lifecycle = %s releases=%v", got, syncStub.releases)
	}
	synced := syncStub.synced[0]
	if synced.Path != "edited.go" || synced.JobID != "job-sync" || synced.SessionID != "parent-coord" || synced.Turn != 3 || synced.ToolCallID != "call-promote-1" {
		t.Fatalf("synced mismatch: %+v", synced)
	}

	if len(ledger.records) != 1 {
		t.Fatalf("expected 1 ledger record, got %d", len(ledger.records))
	}
	rec := ledger.records[0]
	if rec.TextBefore == nil || rec.TextBefore.Revision != 1 || rec.TextAfter == nil || rec.TextAfter.Revision != 2 {
		t.Fatalf("TextBefore/TextAfter not passed to record: before=%+v after=%+v", rec.TextBefore, rec.TextAfter)
	}
}

func TestFailedPromotionDiscardsEditorImportsAfterRollback(t *testing.T) {
	f := newEditorSyncPromotion(t, &mergeStoreStub{commitErr: errors.New("durable promotion rejected")})
	var diskAtRelease string
	f.docs.onRelease = func() {
		raw, _ := os.ReadFile(filepath.Join(f.primary, "edited.go"))
		diskAtRelease = string(raw)
	}
	if _, err := f.svc.PromoteOverlay(context.Background(), "parent-coord", "job-sync", api.PromoteOverlayInput{}); err == nil {
		t.Fatal("promotion succeeded despite a failed durable commit")
	}
	if len(f.docs.releases) != 1 || f.docs.releases[0] {
		t.Fatalf("editor imports were not discarded: releases=%v events=%v", f.docs.releases, f.docs.events)
	}
	if diskAtRelease != "base\n" {
		t.Fatalf("hold released before the rollback: disk=%q", diskAtRelease)
	}
	if len(f.ledger.records) != 0 {
		t.Fatalf("failed promotion recorded source history: %+v", f.ledger.records)
	}
}
