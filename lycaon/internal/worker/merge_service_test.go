package worker_test

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

type mergeQueueStub struct {
	task *api.WorkerTask
}

func (s *mergeQueueStub) Get(jobID string) (*api.WorkerTask, bool) {
	if s.task == nil || s.task.ID != jobID {
		return nil, false
	}
	cp := *s.task
	return &cp, true
}

func (s *mergeQueueStub) EnsureWorkerBranch(ctx context.Context, jobID string) (*api.WorkerTask, *worker.BranchLease, error) {
	task, ok := s.Get(jobID)
	if !ok {
		return nil, nil, worker.ErrWorkerBranchUnavailable
	}
	lease, err := worker.LeaseExistingBranch(ctx, task)
	if err != nil {
		return nil, nil, err
	}
	return task, lease, nil
}

type mergeStoreStub struct {
	status    api.WorkerMergeStatus
	cleared   bool
	claimed   bool
	released  bool
	commitErr error
}

func (s *mergeStoreStub) BeginMergeApply(context.Context, string) (string, bool, error) {
	s.claimed = true
	return "stub-claim", true, nil
}

func (s *mergeStoreStub) RenewMergeApply(context.Context, string, string) (bool, error) {
	return true, nil
}

func (s *mergeStoreStub) ReleaseMergeApply(_ context.Context, _ string, _ string) error {
	s.released = true
	s.status = api.WorkerMergeStatusPending
	return nil
}

func (s *mergeStoreStub) ReclaimExpiredMergeApply(context.Context, string) (string, bool, error) {
	return "stub-reclaim", true, nil
}

func (s *mergeStoreStub) ListMergeApplying(context.Context) ([]string, error) {
	return nil, nil
}

func (s *mergeStoreStub) SetMergeStatus(_ context.Context, _ string, status api.WorkerMergeStatus) error {
	s.status = status
	return nil
}

func (s *mergeStoreStub) SetMergeStatuses(_ context.Context, updates []worker.MergeStatusUpdate) error {
	if len(updates) > 0 {
		s.status = updates[0].Status
	}
	return nil
}

func (s *mergeStoreStub) CommitPromotion(ctx context.Context, _ string, _ string, commit worker.PromotionCommit) error {
	if commit.Documents != nil {
		if err := commit.Documents.CommitTx(ctx, nil); err != nil {
			return err
		}
	}
	if s.commitErr != nil {
		return s.commitErr
	}
	for _, record := range commit.Records {
		if commit.Recorder != nil {
			if err := commit.Recorder.RecordTx(ctx, nil, record); err != nil {
				return err
			}
		}
	}
	delivery, err := sourcefeed.EmitBatchTx(ctx, nil, commit.Changes)
	if err != nil {
		return err
	}
	delivery.DeliverCommitted()
	s.status = api.WorkerMergeStatusMerged
	return nil
}

func (s *mergeStoreStub) ClearWorkerWorkspace(context.Context, string) error {
	s.cleared = true
	return nil
}

func mergeRejectFmt(t *testing.T) *guidance.StaticRejectFormatter {
	t.Helper()
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfig", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	return guidance.NewStaticRejectFormatter(cfg)
}

func workspaceBaselinePath(t *testing.T, dir string) string {
	t.Helper()
	snap := testbaseline.Capture(t, dir)
	raw := snap
	return raw
}

func TestPreviewForSessionListsPaths(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	branch := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-1")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branch, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branch, "a.go"), []byte("package a\n"), 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.go"}}
	revision, rootDigest := invocation.SourceRevisionForRoot(branch)
	task := &api.WorkerTask{
		ID:                    "job-1",
		ChildSessionID:        "child-1",
		ParentSessionID:       "parent-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         dir,
		WorkspaceRoot:         branch,
		MergeStatus:           api.WorkerMergeStatusPending,
		AgentType:             "implementer",
		Scope:                 &scope,
		WorkspaceBaselinePath: workspaceBaselinePath(t, dir),
		Status:                api.WorkerStatusComplete,
	}
	svc := &worker.MergeService{
		Queue:  &mergeQueueStub{task: task},
		Reject: mergeRejectFmt(t),
		Reports: worker.ChangeReportDeps{
			Messages: func(context.Context, string) ([]api.Message, error) {
				return []api.Message{{
					Role: api.MessageRoleTool,
					ToolResult: &api.ToolResult{
						Content: `{"outcome":"passed"}`,
						Invocation: &api.InvocationReceipt{
							ID: "receipt-1", Tool: "verify", Status: api.InvocationStatusCompleted,
							SourceRevision: revision, SourceRootDigest: rootDigest,
							SourceVerdict: api.SourceVerdictPassed,
						},
					},
				}}, nil
			},
		},
	}
	svc.Evidence = worker.SourceEvidenceContext{
		SourceRevision: func(context.Context, *api.WorkerTask) workercompletion.SourceRevision {
			return workercompletion.SourceRevision{Revision: revision, RootDigest: rootDigest}
		},
	}
	out, err := svc.PreviewForSession(ctx, "parent-1", "job-1", "hunks", nil)
	testutil.FailErr(t, "PreviewForSession", err)
	if out.Status != api.WorkerMergeStatusPending {
		t.Fatalf("preview status=%q want pending", out.Status)
	}
	if len(out.Paths) == 0 {
		t.Fatal("expected paths from change report or scope")
	}
	if out.SourceEvidence == nil || out.SourceEvidence.Status != "satisfied" {
		t.Fatalf("preview evidence=%+v", out.SourceEvidence)
	}
}

func TestPreviewForSessionPublishesUnmetEvidence(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	branch := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-unmet")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branch, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branch, "a.go"), []byte("package a\n"), 0o644))
	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.go"}}
	task := &api.WorkerTask{
		ID:                    "job-unmet",
		ParentSessionID:       "parent-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         dir,
		WorkspaceRoot:         branch,
		MergeStatus:           api.WorkerMergeStatusPending,
		AgentType:             "implementer",
		Scope:                 &scope,
		WorkspaceBaselinePath: workspaceBaselinePath(t, dir),
		Status:                api.WorkerStatusComplete,
	}
	svc := &worker.MergeService{
		Queue: &mergeQueueStub{task: task},

		Reject: mergeRejectFmt(t),
	}
	out, err := svc.PreviewForSession(ctx, "parent-1", "job-unmet", "hunks", nil)
	testutil.FailErr(t, "PreviewForSession", err)
	if out.SourceEvidence == nil || out.SourceEvidence.Status != "unmet" {
		t.Fatalf("source_evidence = %+v want unmet", out.SourceEvidence)
	}
}

func TestPromoteOverlayPromotesAndClears(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	branch := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-2")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branch, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branch, "b.go"), []byte("package b\n"), 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"b.go"}}
	task := &api.WorkerTask{
		ID:                    "job-2",
		ParentSessionID:       "parent-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         dir,
		WorkspaceRoot:         branch,
		MergeStatus:           api.WorkerMergeStatusPending,
		AgentType:             "implementer",
		Scope:                 &scope,
		WorkspaceBaselinePath: workspaceBaselinePath(t, dir),
		Status:                api.WorkerStatusComplete,
	}
	store := &mergeStoreStub{}
	svc := &worker.MergeService{
		Queue: &mergeQueueStub{task: task},

		Store:  store,
		Reject: mergeRejectFmt(t),
	}
	out, err := svc.PromoteOverlay(ctx, "parent-1", "job-2", api.PromoteOverlayInput{})
	testutil.FailErr(t, "PromoteOverlay", err)
	if out.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("merge status=%q want merged", out.Status)
	}
	if store.status != api.WorkerMergeStatusMerged || !store.cleared {
		t.Fatalf("store status=%q cleared=%v", store.status, store.cleared)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.go")); err != nil {
		t.Fatalf("expected promoted file on primary tree: %v", err)
	}
}

func TestPromoteOverlayRemovesDeletedPath(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	primaryFile := filepath.Join(dir, "gone.go")
	testutil.FailErr(t, "WriteFile primary", os.WriteFile(primaryFile, []byte("package gone\n"), 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"gone.go"}}
	baseline := workspaceBaselinePath(t, dir)

	branch := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-del")
	testutil.FailErr(t, "MkdirAll branch", os.MkdirAll(branch, 0o755))
	testutil.FailErr(t, "WriteJobMeta", workspace.WriteJobMeta(enginepaths.MetaDirForBranchRoot(branch), workspace.JobMeta{
		Roots:            []workspace.JobMetaRoot{{ID: "root", Path: dir, IsPrimary: true}},
		SnapshotComplete: true,
	}))

	task := &api.WorkerTask{
		ID:                    "job-del",
		ParentSessionID:       "parent-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         dir,
		WorkspaceRoot:         branch,
		MergeStatus:           api.WorkerMergeStatusPending,
		AgentType:             "implementer",
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
		Status:                api.WorkerStatusComplete,
	}
	store := &mergeStoreStub{}
	svc := &worker.MergeService{
		Queue: &mergeQueueStub{task: task},

		Store:  store,
		Reject: mergeRejectFmt(t),
	}
	out, err := svc.PromoteOverlay(ctx, "parent-1", "job-del", api.PromoteOverlayInput{})
	testutil.FailErr(t, "PromoteOverlay", err)
	if out.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("merge status=%q want merged", out.Status)
	}
	if _, err := os.Stat(primaryFile); !os.IsNotExist(err) {
		t.Fatalf("expected gone.go removed from primary tree, stat err=%v", err)
	}
}

func TestPromoteOverlayAppliesExplicitConflictResolution(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	branch := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-conflict")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branch, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branch, "c.go"), []byte("branch\n"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(dir, "c.go"), []byte("primary-changed\n"), 0o644))

	baseline := testbaseline.FromFiles(t, map[string]testbaseline.File{"c.go": {Content: "original\n"}})
	task := &api.WorkerTask{
		ID:                    "job-conflict",
		ParentSessionID:       "parent-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         dir,
		WorkspaceRoot:         branch,
		MergeStatus:           api.WorkerMergeStatusPending,
		AgentType:             "implementer",
		Scope:                 &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"c.go"}},
		WorkspaceBaselinePath: baseline,
		Status:                api.WorkerStatusComplete,
	}
	svc := &worker.MergeService{
		Queue: &mergeQueueStub{task: task},

		Store:  &mergeStoreStub{},
		Reject: mergeRejectFmt(t),
	}
	out, err := svc.PromoteOverlay(ctx, "parent-1", "job-conflict", api.PromoteOverlayInput{Resolutions: []api.WorkerPromoteResolution{{Path: "c.go", Action: api.WorkerPromoteResolutionActionKeepTheirs}}})
	testutil.FailErr(t, "PromoteOverlay", err)
	if out.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("status=%q want merged", out.Status)
	}
}

func TestPromoteOverlayWritesResolvedContent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	branch := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-resolve")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branch, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branch, "e.go"), []byte("branch\n"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(dir, "e.go"), []byte("primary-changed\n"), 0o644))

	baseline := testbaseline.FromFiles(t, map[string]testbaseline.File{"e.go": {Content: "primary\n"}})
	task := &api.WorkerTask{
		ID:                    "job-resolve",
		ParentSessionID:       "parent-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         dir,
		WorkspaceRoot:         branch,
		MergeStatus:           api.WorkerMergeStatusPending,
		AgentType:             "implementer",
		Scope:                 &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"e.go"}},
		WorkspaceBaselinePath: baseline,
		Status:                api.WorkerStatusComplete,
	}
	svc := &worker.MergeService{
		Queue: &mergeQueueStub{task: task},

		Store:  &mergeStoreStub{},
		Reject: mergeRejectFmt(t),
	}
	out, err := svc.PromoteOverlay(ctx, "parent-1", "job-resolve", api.PromoteOverlayInput{
		Resolutions: []api.WorkerPromoteResolution{{Path: "e.go", Content: "merged\n"}},
	})
	testutil.FailErr(t, "PromoteOverlay", err)
	if out.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("resolve status=%q want merged", out.Status)
	}
	data, err := os.ReadFile(filepath.Join(dir, "e.go"))
	testutil.FailErr(t, "ReadFile primary e.go", err)
	if string(data) != "merged\n" {
		t.Fatalf("primary content=%q want merged version", string(data))
	}
}

func TestPromoteOverlayAppliesCleanAndResolvedPathsTogether(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	branch := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-partial")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branch, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branch, "safe.go"), []byte("safe\n"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branch, "conflict.go"), []byte("branch\n"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(dir, "conflict.go"), []byte("primary-changed\n"), 0o644))

	baseline := testbaseline.FromFiles(t, map[string]testbaseline.File{"safe.go": {Content: ""}, "conflict.go": {Content: "original\n"}})
	task := &api.WorkerTask{
		ID:                    "job-partial",
		ParentSessionID:       "parent-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         dir,
		WorkspaceRoot:         branch,
		MergeStatus:           api.WorkerMergeStatusPending,
		AgentType:             "implementer",
		Scope:                 &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"safe.go", "conflict.go"}},
		WorkspaceBaselinePath: baseline,
		Status:                api.WorkerStatusComplete,
	}
	store := &mergeStoreStub{}
	svc := &worker.MergeService{
		Queue: &mergeQueueStub{task: task},

		Store:  store,
		Reject: mergeRejectFmt(t),
	}
	out, err := svc.PromoteOverlay(ctx, "parent-1", "job-partial", api.PromoteOverlayInput{Resolutions: []api.WorkerPromoteResolution{{Path: "conflict.go", Action: api.WorkerPromoteResolutionActionKeepTheirs}}})
	testutil.FailErr(t, "PromoteOverlay", err)
	if out.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("status=%q want merged", out.Status)
	}
	if _, err := os.Stat(filepath.Join(dir, "safe.go")); err != nil {
		t.Fatalf("clean path not promoted: %v", err)
	}
	if store.status != api.WorkerMergeStatusMerged {
		t.Fatalf("store status=%q want merged", store.status)
	}
}

func TestPreviewForSessionReportsConflicts(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	branch := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-prev")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branch, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branch, "d.go"), []byte("branch\n"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(dir, "d.go"), []byte("primary-changed\n"), 0o644))

	task := &api.WorkerTask{
		ID:                    "job-prev",
		ParentSessionID:       "parent-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         dir,
		WorkspaceRoot:         branch,
		MergeStatus:           api.WorkerMergeStatusPending,
		Scope:                 &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"d.go"}},
		WorkspaceBaselinePath: testbaseline.FromFiles(t, map[string]testbaseline.File{"d.go": {Content: "original\n"}}),
		Status:                api.WorkerStatusComplete,
	}
	svc := &worker.MergeService{
		Queue: &mergeQueueStub{task: task},

		Reject: mergeRejectFmt(t),
	}
	out, err := svc.PreviewForSession(ctx, "parent-1", "job-prev", "hunks", []string{"d.go"})
	testutil.FailErr(t, "PreviewForSession", err)
	if len(out.Conflicts) != 1 || out.Conflicts[0].Path != "d.go" {
		t.Fatalf("conflicts=%v", out.Conflicts)
	}
}

func TestPromoteOverlayRejectsWrongSession(t *testing.T) {
	ctx := context.Background()
	task := &api.WorkerTask{
		ID:              "job-3",
		ParentSessionID: "parent-1",
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspaceRoot:   filepath.Join(t.TempDir(), "branch"),
		MergeStatus:     api.WorkerMergeStatusPending,
		Scope:           &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"x.go"}},
	}
	svc := &worker.MergeService{
		Queue: &mergeQueueStub{task: task},

		Reject: mergeRejectFmt(t),
	}
	_, err := svc.PromoteOverlay(ctx, "other-session", "job-3", api.PromoteOverlayInput{})
	if err == nil || !strings.Contains(err.Error(), worker.OverlayPromoteSessionMismatchCode) {
		t.Fatalf("expected session mismatch: %v", err)
	}
}

func TestPromoteOverlayMergesVacuousBranch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	branch := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-vacuous")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branch, 0o755))
	content := []byte("package noop\n")
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(dir, "noop.go"), content, 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branch, "noop.go"), content, 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"noop.go"}}
	task := &api.WorkerTask{
		ID:                    "job-vacuous",
		ParentSessionID:       "parent-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         dir,
		WorkspaceRoot:         branch,
		MergeStatus:           api.WorkerMergeStatusPending,
		AgentType:             "implementer",
		Scope:                 &scope,
		WorkspaceBaselinePath: workspaceBaselinePath(t, dir),
		Status:                api.WorkerStatusComplete,
	}
	store := &mergeStoreStub{}
	svc := &worker.MergeService{
		Queue: &mergeQueueStub{task: task},

		Store:  store,
		Reject: mergeRejectFmt(t),
	}
	out, err := svc.PromoteOverlay(ctx, "parent-1", "job-vacuous", api.PromoteOverlayInput{})
	testutil.FailErr(t, "PromoteOverlay", err)
	if out.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("merge status=%q want merged (no silent skip)", out.Status)
	}
	if store.status != api.WorkerMergeStatusMerged || !store.cleared {
		t.Fatalf("store status=%q cleared=%v", store.status, store.cleared)
	}
}

func lineShiftMergeFixture(t *testing.T) (context.Context, string, *api.WorkerTask) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	branch := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-shift")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branch, 0o755))
	testutil.FailErr(t, "WriteFile safe.go", os.WriteFile(filepath.Join(branch, "safe.go"), []byte("safe\n"), 0o644))

	base := strings.Repeat("line\n", 50)
	primary := strings.Repeat("line\n", 55)
	branchBody := strings.Repeat("line\n", 50) + "trap\n"
	testutil.FailErr(t, "WriteFile shifted.py branch", os.WriteFile(filepath.Join(branch, "shifted.py"), []byte(branchBody), 0o644))
	testutil.FailErr(t, "WriteFile shifted.py primary", os.WriteFile(filepath.Join(dir, "shifted.py"), []byte(primary), 0o644))

	baseline := fmt.Sprintf(`{"safe.go":{"content":""},"shifted.py":{"content":%q}}`, base)
	task := &api.WorkerTask{
		ID:                    "job-shift",
		ParentSessionID:       "parent-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         dir,
		WorkspaceRoot:         branch,
		MergeStatus:           api.WorkerMergeStatusPending,
		AgentType:             "implementer",
		Scope:                 &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"safe.go", "shifted.py"}},
		WorkspaceBaselinePath: baseline,
		Status:                api.WorkerStatusComplete,
	}
	return ctx, dir, task
}

func TestPromoteOverlayInvalidResolutionLeavesCleanPathsUntouched(t *testing.T) {
	ctx, dir, task := lineShiftMergeFixture(t)
	dataDir := t.TempDir()
	svc := &worker.MergeService{
		Queue: &mergeQueueStub{task: task},

		Reject:  mergeRejectFmt(t),
		DataDir: dataDir,
	}
	_, err := svc.PromoteOverlay(ctx, "parent-1", "job-shift", api.PromoteOverlayInput{
		Resolutions: []api.WorkerPromoteResolution{{
			Path: "shifted.py",
			Hunks: []api.WorkerPromoteHunkResolution{{
				StartLine: 1_000_000,
				EndLine:   1_000_000,
				Content:   "invalid\n",
			}},
		}},
	})
	if err == nil {
		t.Fatal("expected invalid resolution to fail")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "safe.go")); !os.IsNotExist(statErr) {
		t.Fatalf("clean path changed before conflict resolution: %v", statErr)
	}
}
