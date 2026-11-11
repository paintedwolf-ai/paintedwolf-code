package session_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMergeReconcilePathsAllowCoordinatorWrite(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src", "main.go")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(filepath.Dir(src), 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(src, []byte("package main\n"), 0o644))

	profiles := []sandbox.ToolProfile{{
		ID:         "coordinator",
		WriteGlobs: []string{settingsoverlay.Rel("blueprints/**")},
	}}
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, profiles)

	mgr := session.NewHost(sessionstore.NewMemory(), session.Models{Client: nil, Provider: nil, Limits: settings.SessionLimits{}, Cost: nil}, nil)
	mgr.Promotion.SetMergeReconcilePaths("sess-1", []string{"src/main.go"})
	boundary.SetMergeReconcileAllowlister(mgr.Promotion)

	ctx := sandbox.WithSessionID(context.Background(), "sess-1")
	if err := boundary.AssertWriteScope(ctx, root, "src/main.go", "coordinator"); err != nil {
		t.Fatalf("expected reconcile write allowed: %v", err)
	}
	if err := boundary.AssertWriteScope(ctx, root, "src/other.go", "coordinator"); err == nil {
		t.Fatal("expected write outside reconcile set to fail")
	}
}

func TestMergeReconcilePathsNotRegisteredForInvestigateProductWrite(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src", "foo.go")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(filepath.Dir(src), 0o755))

	mgr := session.NewHost(sessionstore.NewMemory(), session.Models{Client: nil, Provider: nil, Limits: settings.SessionLimits{}, Cost: nil}, nil)
	if mgr.Promotion.Allowed("sess-investigate", "src/foo.go") {
		t.Fatal("investigate product path must not be reconcile-allowed without SetMergeReconcilePaths")
	}
}

// [OAR-EVAL-8] Catalog selection uses the producer's observed facts.
func requireCatalogDecision(t *testing.T, anchor string, gc *oar.GuardContext, code string) {
	t.Helper()
	result, err := sessionTestOARPipeline(t).EvaluateBlock(context.Background(), anchor, gc)
	testutil.FailErr(t, "evaluate stock catalog", err)
	got := ""
	if result != nil && result.Decision != nil && result.Decision.Effect == oar.EffectBlock {
		got = result.Decision.Code
	}
	if got != code {
		t.Fatalf("catalog block = %q, want %q", got, code)
	}
}

func TestCatalogStackedBaseUsesValidatedState(t *testing.T) {
	ctx := context.Background()
	q := worker.NewInMemoryQueue(8)
	sess := &api.Session{ID: "catalog-stacked-parent", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), AgentType: "coordinator", Posture: api.SessionPostureBuild}
	job, err := q.Enqueue(ctx, api.WorkerTask{ParentSessionID: sess.ID, ProjectID: sess.ProjectID, WorkspacePath: sess.WorkspacePath, AgentType: "implementer", Prompt: "fixture", Brief: "fixture", Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite}})
	testutil.FailErr(t, "enqueue base", err)
	for _, tc := range []struct{ name, base, agent, mode, code string }{
		{"valid", job, "implementer", "write", ""},
		{"missing", "absent", "implementer", "write", workeradmission.OverlayBaseMissingCode},
		{"scope rejected before base lookup", "absent", "path-explorer", "write", workeradmission.TaskScopeProfileReadOnlyCode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gc := observeTaskInFlight(t, ctx, workeradmission.WorkerCycleGuardDeps{Workers: q}, sess, map[string]any{"agent_type": tc.agent, "scope": map[string]any{"mode": tc.mode, "base_overlay_id": tc.base}})
			requireCatalogDecision(t, oar.AnchorCoordinatorPreInvoke, gc, tc.code)
		})
	}
}

func TestCatalogReadCapacityDoesNotMeanWorkersIdle(t *testing.T) {
	ctx := context.Background()
	q := worker.NewInMemoryQueue(8)
	sess := &api.Session{ID: "catalog-cap-parent", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), AgentType: "coordinator", Posture: api.SessionPostureBuild}
	_, err := q.Enqueue(ctx, api.WorkerTask{ParentSessionID: sess.ID, ProjectID: sess.ProjectID, WorkspacePath: sess.WorkspacePath, AgentType: "path-explorer", Prompt: "fixture", Brief: "fixture", Scope: &api.TaskScope{Mode: api.TaskScopeModeRead}})
	testutil.FailErr(t, "enqueue read worker", err)
	for _, cap := range []int{1, 2} {
		gc := observeTaskInFlight(t, ctx, workeradmission.WorkerCycleGuardDeps{Workers: q, MaxReadWorkers: func(context.Context, string) int { return cap }}, sess, map[string]any{"agent_type": "path-explorer", "scope": map[string]any{"mode": "read"}})
		if gc.Workers.WorkersIdle || gc.Workers.ActiveReadCount != 1 || gc.Workers.ActiveWriteCount != 0 {
			t.Fatalf("roster facts: idle=%v read=%d write=%d", gc.Workers.WorkersIdle, gc.Workers.ActiveReadCount, gc.Workers.ActiveWriteCount)
		}
		code := ""
		if cap == 1 {
			code = workeradmission.CoordinatorWorkerInFlightCode
		}
		requireCatalogDecision(t, oar.AnchorCoordinatorPreInvoke, gc, code)
	}
}

type failedArtifactProbe struct{}

func (failedArtifactProbe) HasChanges(context.Context, string) (bool, error) {
	return false, errors.New("workspace probe unavailable")
}

func TestCatalogArtifactProbeFailureIsNotAbsence(t *testing.T) {
	sess := &api.Session{ID: "artifact-probe-child", ParentSessionID: "parent", AgentType: "implementer", WorkspacePath: t.TempDir()}
	history := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "read-1", Name: "read"}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted}},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "edit-1", Name: "edit"}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeRejected}},
	}
	gc := oar.NewGuardContext()
	workercompletion.ObserveImplementerFinishWithoutWrite(context.Background(), sess, history, "Report", sess.WorkspacePath, failedArtifactProbe{}, gc)
	if gc.Grounding.WorkerArtifactMeasured {
		t.Fatal("failed probe claimed a measurement")
	}
	requireCatalogDecision(t, oar.AnchorWorkerReportCheck, gc, "")
}

func TestCatalogGroundingVerdictsDoNotOverlap(t *testing.T) {
	for _, code := range []string{"COORDINATOR_UNGROUNDED_CLAIM", "COORDINATOR_CRITERIA_UNMET", "COORDINATOR_GROUNDING_ESCALATED"} {
		t.Run(code, func(t *testing.T) {
			gc := oar.NewGuardContext()
			delegation.ObserveDelegationGroundingVerdict(gc, delegation.GroundingVerdict{Code: code, Reason: "Missing {{ receipt }} — résumé", LegID: "fixture-leg"})
			if code == "COORDINATOR_GROUNDING_ESCALATED" {
				result, err := sessionTestOARPipeline(t).EvaluateBlock(t.Context(), oar.AnchorCoordinatorPostTurn, gc)
				testutil.FailErr(t, "evaluate escalation notice", err)
				if result.Decision == nil || result.Decision.Effect != oar.EffectNudge || len(result.Decision.Advisories) != 1 || result.Decision.Advisories[0].Code != code {
					t.Fatalf("post-turn advisory = %+v", result.Decision)
				}
			} else {
				result, err := sessionTestOARPipeline(t).EvaluateBlock(t.Context(), oar.AnchorCoordinatorCloseoutCheck, gc)
				testutil.FailErr(t, "evaluate grounding refusal", err)
				if result.Decision == nil || result.Decision.Code != code || result.Decision.Effect != oar.EffectBlock {
					t.Fatalf("closeout decision = %+v", result.Decision)
				}
				if result.Decision.Copy == nil || result.Decision.Copy["cause"] != "Missing {{ receipt }} — résumé" {
					t.Fatalf("grounding reason was lost or interpreted: %+v", result.Decision.Copy)
				}
			}
		})
	}
}

func TestOverlayChangedPathsHonorsCallerCancellation(t *testing.T) {
	root := t.TempDir()
	baseline := testbaseline.Capture(t, root)
	testutil.FailErr(t, "write change", os.WriteFile(filepath.Join(root, "changed.go"), []byte("changed\n"), 0o600))
	task := &api.WorkerTask{
		WorkspaceRoot: root, WorkspaceBaselinePath: baseline,
		Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite},
	}
	if paths := session.OverlayWorkspaceChangedPaths(t.Context(), task, nil); len(paths) != 1 {
		t.Fatalf("live inspection paths=%v", paths)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if paths := session.OverlayWorkspaceChangedPaths(ctx, task, nil); len(paths) != 0 {
		t.Fatalf("canceled inspection continued: %v", paths)
	}
	if !session.OverlayAwaitingPromote(ctx, task) {
		t.Fatal("canceled inspection treated unresolved work as clean")
	}
}

func TestOverlayChangedPathsIncludesChangesOutsideSuggestedScope(t *testing.T) {
	primary := t.TempDir()
	overlay := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-a")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(overlay, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "a.go"), []byte("branch\n"), 0o644))
	testutil.FailErr(t, "WriteFile extra", os.WriteFile(filepath.Join(overlay, "b.go"), []byte("extra\n"), 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.go"}}
	baseline := testbaseline.Capture(t, primary)

	task := &api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary,
		WorkspaceRoot:         overlay,
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
	}

	paths := session.OverlayWorkspaceChangedPaths(t.Context(), task, nil)
	if len(paths) != 2 || paths[0] != "a.go" || paths[1] != "b.go" {
		t.Fatalf("OverlayWorkspaceChangedPaths = %v want [a.go b.go]", paths)
	}
	if !session.OverlayAwaitingPromote(t.Context(), task) {
		t.Fatal("expected overlay awaiting promote")
	}
}

func TestOverlayAwaitingPromoteWithAbsolutePathSuggestion(t *testing.T) {
	primary := t.TempDir()
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(primary, "index.html"), []byte("v1"), 0o644))

	overlay := t.TempDir()
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "index.html"), []byte("v2-rewritten"), 0o644))
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(filepath.Join(overlay, "src"), 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "src", "game.js"), []byte("game"), 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{primary}}
	baseline := testbaseline.Capture(t, primary)
	if len(baseline) == 0 {
		t.Fatalf("baseline = %+v", baseline)
	}

	task := &api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary,
		WorkspaceRoot:         overlay,
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
	}
	if !session.OverlayAwaitingPromote(t.Context(), task) {
		t.Fatal("whole-repo absolute scope overlay must await promote, not abort")
	}
	paths := session.OverlayWorkspaceChangedPaths(t.Context(), task, nil)
	if len(paths) != 2 {
		t.Fatalf("changed = %v want index.html + src/game.js", paths)
	}
}

func TestOverlayDivergencePathsSafetyNet(t *testing.T) {
	primary := t.TempDir()
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(primary, "index.html"), []byte("v1"), 0o644))

	overlay := t.TempDir()
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "index.html"), []byte("v2-rewritten"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "extra.js"), []byte("new"), 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"untouched.txt"}}
	task := &api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary,
		WorkspaceRoot: overlay,
		Scope:         &scope,
	}
	div := session.OverlayWorkspaceDivergencePaths(task, nil)
	if len(div) != 2 {
		t.Fatalf("divergence = %v want index.html + extra.js", div)
	}
}

func TestOverlayAwaitingPromoteFalseWithoutDiff(t *testing.T) {
	primary := t.TempDir()
	overlay := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-b")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(overlay, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "a.go"), []byte("same\n"), 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.go"}}
	baseline := testbaseline.Capture(t, overlay)

	task := &api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary,
		WorkspaceRoot:         overlay,
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
	}
	if session.OverlayAwaitingPromote(t.Context(), task) {
		t.Fatal("expected no promote when overlay matches baseline")
	}
}

func TestOverlayWorkspaceAvailableRejectsSymlinkRoot(t *testing.T) {
	branch := filepath.Join(t.TempDir(), "branch")
	if err := os.Symlink(t.TempDir(), branch); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if session.OverlayWorkspaceAvailable(&api.WorkerTask{WorkspaceRoot: branch}) {
		t.Fatal("OverlayWorkspaceAvailable accepted a symlink root")
	}
}

func TestOverlayAwaitingPromoteFalseWithoutChanges(t *testing.T) {
	primary := t.TempDir()
	overlay := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-absent")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(overlay, 0o755))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"test-chess.js"}}
	baseline := testbaseline.Capture(t, primary)

	task := &api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary,
		WorkspaceRoot:         overlay,
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
	}
	if session.OverlayAwaitingPromote(t.Context(), task) {
		t.Fatal("expected no promote without changes")
	}
}

func TestOverlayAwaitingPromoteFalseWhenWorkspaceGone(t *testing.T) {
	primary := t.TempDir()
	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.go"}}
	baseline := testbaseline.Capture(t, primary)

	task := &api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary,
		WorkspaceRoot:         filepath.Join(primary, settingsoverlay.DirName(), "overlays", "gone"),
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
	}
	if session.OverlayAwaitingPromote(t.Context(), task) {
		t.Fatal("expected false when branch directory is missing")
	}
}

func TestResolveWorkerSummaryStatus(t *testing.T) {
	primary := t.TempDir()
	overlay := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-c")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(overlay, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "a.go"), []byte("new\n"), 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.go"}}
	baseline := testbaseline.Capture(t, primary)

	task := &api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary,
		WorkspaceRoot:         overlay,
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
	}
	got := session.ResolveWorkerSummaryStatus(t.Context(), api.WorkerSummaryStatusComplete, task)
	if got != api.WorkerSummaryStatusOpen {
		t.Fatalf("eligible status = %q want open", got)
	}
	got = session.ResolveWorkerSummaryStatus(t.Context(), api.WorkerSummaryStatusComplete, task)
	if got != api.WorkerSummaryStatusOpen {
		t.Fatalf("unmet status = %q want partial", got)
	}
}
