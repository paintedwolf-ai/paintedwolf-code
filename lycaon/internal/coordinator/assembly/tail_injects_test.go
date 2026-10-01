package assembly

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/prompttest"
	"github.com/lycaon/lycaon/pkg/api"
)

type advancingTurnFrameSource struct {
	phase    string
	revision int64
}

func (s *advancingTurnFrameSource) BuildCoordinatorTurnFrame(_ context.Context, _ string, _ *api.Session) (inject.CoordinatorTurnFrame, error) {
	return inject.CoordinatorTurnFrame{
		WorkflowRevision: s.revision,
		RunContext: api.CoordinatorRunContext{
			WorkflowID: "atomic", WorkflowVersion: "1", RunID: "run-1",
			RunStatus: "running", CurrentPhase: s.phase,
		},
		Runtime: inject.WorkflowRuntimeSnapshot{
			Phases: []inject.WorkflowPhaseRow{
				{ID: "boot", Next: "work"},
				{ID: "work", CompleteWhen: "done", Terminal: true},
			},
			PhaseExit: &inject.PhaseExitView{Kind: s.phase + "-exit"},
		},
	}, nil
}

type advancingBoard struct{}

func (advancingBoard) PrependBoardIfChanged(_ context.Context, _ *api.Session, run api.CoordinatorRunContext) (string, bool) {
	return "board phase=" + run.CurrentPhase, true
}
func (advancingBoard) WorkerBoard(context.Context, *api.Session) (string, bool) {
	return "", false
}
func (advancingBoard) BoardInjectHash(string) string { return "board-v1" }
func (advancingBoard) InvalidateOrientation(string)  {}

type advancingOrientRecorder struct{ source *advancingTurnFrameSource }

func (r advancingOrientRecorder) RecordBoardOrientReady(context.Context, string, string) error {
	r.source.phase = "work"
	r.source.revision++
	return nil
}

// TestDynamicInjectsFollowHistory keeps dynamic injects after history.
func TestDynamicInjectsFollowHistory(t *testing.T) {
	eng := prefixStabilityEngine(t)
	sess := &api.Session{
		ID:            "tail-injects",
		Posture:       api.SessionPostureBuild,
		AgentType:     orchestration.ProfileCoordinator,
		WorkspacePath: t.TempDir(),
	}
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "first user turn"},
		{Role: api.MessageRoleAssistant, Content: "assistant reply"},
		{Role: api.MessageRoleUser, Content: "second user turn"},
	}

	eng.BeginPromptTurn(sess.ID, "")
	msgs, err := eng.BuildCompletionMessages(context.Background(), sess, history, nil)
	if err != nil {
		t.Fatalf("BuildCompletionMessages: %v", err)
	}

	lastHistoryIdx := -1
	for i, m := range msgs {
		if m.Content == "second user turn" {
			lastHistoryIdx = i
		}
	}
	if lastHistoryIdx < 0 {
		t.Fatal("history tail not found in assembled messages")
	}

	for i, m := range msgs[:lastHistoryIdx] {
		if isVolatilePromptBlock(m.Content) {
			t.Fatalf("dynamic inject at index %d precedes history tail at %d:\n%s", i, lastHistoryIdx, m.Content[:120])
		}
		if strings.Contains(m.Content, inject.ActiveWorkflowInjectSentinel) {
			t.Fatalf("workflow runtime block at index %d must follow history", i)
		}
	}

	foundVolatileAfter := false
	for _, m := range msgs[lastHistoryIdx+1:] {
		if isVolatilePromptBlock(m.Content) {
			foundVolatileAfter = true
		}
	}
	if !foundVolatileAfter {
		t.Fatal("expected workflow runtime inject after history for an active-workflow session")
	}

	if breakIdx := promptCacheMarkedIndex(msgs); breakIdx != lastHistoryIdx {
		t.Fatalf("PromptCacheBreakpoint at %d, want last history message at %d", breakIdx, lastHistoryIdx)
	}
}

func TestBoardOrientationRefreshesAtomicTurnFrameBeforeRender(t *testing.T) {
	root := prefixStabilityTestRoot(t)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	source := &advancingTurnFrameSource{phase: "boot", revision: 1}
	eng := &AssemblyEngine{}
	eng.SetDeps(AssemblyDeps{
		Prompts: pe, Injects: prompts.NewInjectRenderer(pe),
		Limits:           func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		CoordinatorFrame: source,
		Board:            advancingBoard{},
		BoardOrientReady: advancingOrientRecorder{source: source},
		PromptToolLister: prompttest.CoordinatorTools,
	})
	sess := &api.Session{ID: "atomic-frame", AgentType: orchestration.ProfileCoordinator, WorkspacePath: t.TempDir()}
	eng.BeginPromptTurn(sess.ID, "")
	msgs, err := eng.BuildCompletionMessages(context.Background(), sess, nil, nil)
	testutil.FailErr(t, "build completion messages", err)
	joined := ""
	for _, msg := range msgs {
		joined += msg.Content + "\n"
	}
	if strings.Contains(joined, "phase 1/2 — `boot`") || strings.Contains(joined, "board phase=boot") {
		t.Fatalf("prompt retained stale boot revision:\n%s", joined)
	}
	if !strings.Contains(joined, "phase 2/2 — `work`") || !strings.Contains(joined, "board phase=work") {
		t.Fatalf("prompt missing refreshed work revision:\n%s", joined)
	}
}

func TestWorkerPackBoardFollowsHistory(t *testing.T) {
	root := prefixStabilityTestRoot(t)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	inj := prompts.NewInjectRenderer(pe)
	boardEng := NewBoardEngine(placementStubBoardBuilder{hash: "worker-board"}, nil, nil)
	boardEng.SetInjectRenderer(inj)
	eng := &AssemblyEngine{}
	eng.SetDeps(AssemblyDeps{
		Prompts: pe,
		Injects: inj,
		Limits:  func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		Board:   boardEng,
	})
	sess := &api.Session{
		ID: "worker-board-tail", ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID,
		Posture: api.SessionPostureBuild, AgentType: "implementer", WorkspacePath: t.TempDir(),
	}
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "first user turn"},
		{Role: api.MessageRoleAssistant, Content: "assistant reply"},
		{Role: api.MessageRoleUser, Content: "second user turn"},
	}
	eng.BeginPromptTurn(sess.ID, "")
	msgs, err := eng.BuildCompletionMessages(context.Background(), sess, history, nil)
	testutil.FailErr(t, "build completion messages", err)

	lastHistoryIdx := -1
	boardIdx := -1
	for i, m := range msgs {
		if m.Content == "second user turn" {
			lastHistoryIdx = i
		}
		if strings.Contains(m.Content, packboard.PackBoardSentinel) ||
			strings.Contains(m.Content, inject.BoardOrientationInjectSentinel) {
			if boardIdx < 0 {
				boardIdx = i
			}
		}
	}
	if lastHistoryIdx < 0 {
		t.Fatal("history tail not found in assembled messages")
	}
	if boardIdx < 0 {
		t.Fatal("expected worker pack board in assembled messages")
	}
	if boardIdx <= lastHistoryIdx {
		t.Fatalf("worker pack board at %d must follow history tail at %d", boardIdx, lastHistoryIdx)
	}
	if breakIdx := promptCacheMarkedIndex(msgs); breakIdx != lastHistoryIdx {
		t.Fatalf("PromptCacheBreakpoint at %d, want last history message at %d", breakIdx, lastHistoryIdx)
	}
}
