package session

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/promptresult"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/pkg/api"
)

type recordingWorkflowView struct {
	calls         []string
	feedbackErr   error
	completionErr error
	topologyErr   error
	closeoutID    string
}

func (r *recordingWorkflowView) record(name string) {
	r.calls = append(r.calls, name)
}

func (r *recordingWorkflowView) AssertSessionRunnable(ctx context.Context, sessionID string) error {
	r.record("AssertSessionRunnable")
	return nil
}

func (r *recordingWorkflowView) CurrentPhase(ctx context.Context, sessionID string) string {
	r.record("CurrentPhase")
	return "phase-a"
}

func (r *recordingWorkflowView) ActiveReviewVerdictPending(ctx context.Context, sessionID string) bool {
	r.record("ActiveReviewVerdictPending")
	return false
}

func (r *recordingWorkflowView) ActiveCloseoutGateState(ctx context.Context, sessionID string) WorkflowCloseoutGateState {
	r.record("ActiveCloseoutGateState")
	return WorkflowCloseoutGateState{}
}

func (r *recordingWorkflowView) ResolvedRequest(ctx context.Context, sessionID string) ResolvedWorkflowRequest {
	r.record("ResolvedRequest")
	return ResolvedWorkflowRequest{}
}

func (r *recordingWorkflowView) ActivePhaseHasReviewLoop(ctx context.Context, sessionID string) bool {
	r.record("ActivePhaseHasReviewLoop")
	return false
}

func (r *recordingWorkflowView) ActivePhaseGuardState(ctx context.Context, sessionID string) WorkflowPhaseGuardState {
	r.record("ActivePhaseGuardState")
	return WorkflowPhaseGuardState{Phase: "phase-a"}
}

func (r *recordingWorkflowView) ActiveManifest(ctx context.Context, sessionID string) (ActiveWorkflowManifest, bool) {
	r.record("ActiveManifest")
	return ActiveWorkflowManifest{Rules: []string{"manifest-rules.yaml"}}, true
}

func (r *recordingWorkflowView) ParallelTaskMaxWorkers(ctx context.Context, sessionID string) int {
	r.record("ParallelTaskMaxWorkers")
	return 0
}

func (r *recordingWorkflowView) ParallelTaskMaxReadWorkers(ctx context.Context, sessionID string) int {
	r.record("ParallelTaskMaxReadWorkers")
	return 0
}

func (r *recordingWorkflowView) ParallelTaskMaxWriteWorkers(ctx context.Context, sessionID string) int {
	r.record("ParallelTaskMaxWriteWorkers")
	return 0
}

func (r *recordingWorkflowView) PhaseTouchPaths(ctx context.Context, sessionID string) []string {
	r.record("PhaseTouchPaths")
	return nil
}

func (r *recordingWorkflowView) ScaffoldVarsForSession(ctx context.Context, sessionID string) (map[string]any, error) {
	r.record("ScaffoldVarsForSession")
	return map[string]any{"k": "v"}, nil
}

func (r *recordingWorkflowView) ActivePhaseRequiresEvidence(ctx context.Context, sessionID, evidenceType string) bool {
	r.record("ActivePhaseRequiresEvidence")
	return false
}

func (r *recordingWorkflowView) GetActive(ctx context.Context, sessionID string) (*api.WorkflowRun, error) {
	r.record("GetActive")
	return nil, nil
}

func (r *recordingWorkflowView) IsAmbientRun(*api.WorkflowRun) bool {
	r.record("IsAmbientRun")
	return false
}

func (r *recordingWorkflowView) TrySlashPrompt(ctx context.Context, sessionID, text, submissionID string) (*promptresult.Result, bool, error) {
	r.record("TrySlashPrompt")
	return nil, false, nil
}

func (r *recordingWorkflowView) AcceptsEmptyRequest(context.Context, string) bool {
	return false
}

func (r *recordingWorkflowView) PrepareUserRequest(_ context.Context, _, text string) (string, *promptresult.Result, bool, error) {
	return text, nil, false, nil
}

func (r *recordingWorkflowView) TryResolveUserFeedback(ctx context.Context, sessionID, messageID, authorPersonID, message string) error {
	r.record("TryResolveUserFeedback")
	return r.feedbackErr
}

func (r *recordingWorkflowView) StampAndAppendMessages(ctx context.Context, sessionID string, msgs ...api.Message) error {
	r.record("StampAndAppendMessages")
	return nil
}

func (r *recordingWorkflowView) AnnouncePendingAsk(ctx context.Context, sessionID string) {
	r.record("AnnouncePendingAsk")
}

func (r *recordingWorkflowView) RecordWorkerTerminalProof(ctx context.Context, sessionID, completingJobID, summaryStatus string) error {
	r.record("RecordWorkerTerminalProof")
	return nil
}

func (r *recordingWorkflowView) RecordBoardOrientReady(ctx context.Context, sessionID, injectKey string) error {
	r.record("RecordBoardOrientReady")
	return nil
}

func (r *recordingWorkflowView) ReconcileTurnCompletion(context.Context, string) error {
	r.record("ReconcileTurnCompletion")
	return r.completionErr
}

func (r *recordingWorkflowView) MaybeDeliverTopologyReport(ctx context.Context, sessionID, messageID string) error {
	r.record("MaybeDeliverTopologyReport")
	r.closeoutID = messageID
	return r.topologyErr
}

func (r *recordingWorkflowView) RecordReviewLoopVerdict(ctx context.Context, sessionID string, verdict map[string]string, citedEvidence []api.CitationGroundingCitedEvidence) error {
	r.record("RecordReviewLoopVerdict")
	return nil
}

func (r *recordingWorkflowView) ReconcileOrphanedRuns(ctx context.Context, sessionID string) error {
	r.record("ReconcileOrphanedRuns")
	return nil
}

func (r *recordingWorkflowView) ApplyCoordinatorBatchEvent(context.Context, string, batch.Event, int) error {
	r.record("ApplyCoordinatorBatchEvent")
	return nil
}

func (r *recordingWorkflowView) ForgetSession(sessionID string) {
	r.record("ForgetSession")
}

func TestApplyPromptUserTurnPropagatesFeedbackFailure(t *testing.T) {
	ctx := t.Context()
	wantErr := errors.New("feedback store unavailable")
	st := store.NewMemory()
	mgr := NewManager(st, nil, nil, settings.DefaultSessionLimits())
	mgr.SetWorkflowSessionView(&recordingWorkflowView{feedbackErr: wantErr}, nil)
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create session", err)

	_, err = mgr.applyPromptUserTurn(ctx, sess.ID, PromptInput{Text: "answer"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("prompt user turn error = %v, want feedback failure", err)
	}
}

func TestToolpolicyEngineDepsWiresWorkflowView(t *testing.T) {
	view := &recordingWorkflowView{}
	mgr := NewManager(store.NewMemory(), nil, nil, settings.DefaultSessionLimits())
	mgr.SetWorkflowSessionView(view, func(context.Context, string) (toolpolicy.WorkflowSnapshot, error) {
		view.record("snapshot")
		return toolpolicy.WorkflowSnapshot{Phase: "phase-a", AllowedAgents: []string{"coordinator"}, ManifestRules: []string{"manifest-rules.yaml"}, BlueprintPath: "plan-1", PlanContent: "plan body", Vars: map[string]any{"k": "v"}}, nil
	})
	sess := &api.Session{ID: "s1", Posture: api.SessionPostureSpec}

	eval, err := toolpolicy.BuildEvalContext(context.Background(), mgr.toolpolicyEngineDeps(), sess, "read_file", map[string]any{"path": "x"})
	if err != nil {
		t.Fatalf("build policy context: %v", err)
	}
	if eval.Phase != "phase-a" {
		t.Fatalf("phase = %q want phase-a", eval.Phase)
	}
	if len(eval.AllowedAgents) != 1 || eval.AllowedAgents[0] != "coordinator" {
		t.Fatalf("allowed agents = %v", eval.AllowedAgents)
	}
	if len(eval.ManifestRules) != 1 || eval.ManifestRules[0] != "manifest-rules.yaml" {
		t.Fatalf("manifest rules = %v", eval.ManifestRules)
	}
	if eval.BlueprintPath != "plan-1" || eval.PlanContent != "plan body" {
		t.Fatalf("plan = %q %q", eval.BlueprintPath, eval.PlanContent)
	}
	if eval.Vars["k"] != "v" {
		t.Fatalf("vars = %v", eval.Vars)
	}

	want := []string{"snapshot"}
	if len(view.calls) != len(want) {
		t.Fatalf("calls = %v want %v", view.calls, want)
	}
	for i, name := range want {
		if view.calls[i] != name {
			t.Fatalf("call[%d] = %q want %q (all=%v)", i, view.calls[i], name, view.calls)
		}
	}
}
