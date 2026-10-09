package session

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workflowfacts"
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
	archive       string
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

func (r *recordingWorkflowView) ActiveCloseoutGateState(ctx context.Context, sessionID string) workflowfacts.WorkflowCloseoutGateState {
	r.record("ActiveCloseoutGateState")
	return workflowfacts.WorkflowCloseoutGateState{}
}

func (r *recordingWorkflowView) ResolvedRequest(ctx context.Context, sessionID string) workflowfacts.ResolvedWorkflowRequest {
	r.record("ResolvedRequest")
	return workflowfacts.ResolvedWorkflowRequest{}
}

func (r *recordingWorkflowView) ActivePhaseHasReviewLoop(ctx context.Context, sessionID string) bool {
	r.record("ActivePhaseHasReviewLoop")
	return false
}

func (r *recordingWorkflowView) ActivePhaseGuardState(ctx context.Context, sessionID string) workflowfacts.WorkflowPhaseGuardState {
	r.record("ActivePhaseGuardState")
	return workflowfacts.WorkflowPhaseGuardState{Phase: "phase-a"}
}

func (r *recordingWorkflowView) AllowedAgents(ctx context.Context, sessionID string) []string {
	r.record("AllowedAgents")
	return []string{"coordinator"}
}

func (r *recordingWorkflowView) ActiveManifest(ctx context.Context, sessionID string) (workflowfacts.ActiveWorkflowManifest, bool) {
	r.record("ActiveManifest")
	return workflowfacts.ActiveWorkflowManifest{Rules: []string{"manifest-rules.yaml"}, Archive: r.archive}, true
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

func (r *recordingWorkflowView) ActivePlan(ctx context.Context, sessionID string) (string, string, bool) {
	r.record("ActivePlan")
	return "plan-1", "plan body", true
}

func (r *recordingWorkflowView) ActivePhaseRequiresEvidence(ctx context.Context, sessionID, evidenceType string) bool {
	r.record("ActivePhaseRequiresEvidence")
	return false
}

func (r *recordingWorkflowView) ActiveBySession(ctx context.Context, sessionID string) (*api.WorkflowRun, error) {
	r.record("ActiveBySession")
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
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	workflowFixture1 := &recordingWorkflowView{feedbackErr: wantErr}
	mgr.SetWorkflowDomains(&WorkflowDomains{Runs: workflowFixture1, Policy: workflowFixture1, Ambient: workflowFixture1, Blueprints: workflowFixture1, Batch: workflowFixture1, Slash: workflowFixture1, Requests: workflowFixture1, Feedback: workflowFixture1, Transcript: workflowFixture1, Asks: workflowFixture1, Fanout: workflowFixture1, Phases: workflowFixture1, Reports: workflowFixture1, Recovery: workflowFixture1, Cleanup: workflowFixture1})
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create session", err)

	_, err = mgr.Runner.Instructions.Apply(ctx, sess.ID, promptinput.Input{Text: "answer"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("prompt user turn error = %v, want feedback failure", err)
	}
}

func TestToolpolicyEngineDepsWiresWorkflowView(t *testing.T) {
	view := &recordingWorkflowView{}
	mgr := NewHost(store.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	workflowFixture2 := view
	mgr.SetWorkflowDomains(&WorkflowDomains{Runs: workflowFixture2, Policy: workflowFixture2, Ambient: workflowFixture2, Blueprints: workflowFixture2, Batch: workflowFixture2, Slash: workflowFixture2, Requests: workflowFixture2, Feedback: workflowFixture2, Transcript: workflowFixture2, Asks: workflowFixture2, Fanout: workflowFixture2, Phases: workflowFixture2, Reports: workflowFixture2, Recovery: workflowFixture2, Cleanup: workflowFixture2})
	sess := &api.Session{ID: "s1", Posture: api.SessionPostureSpec}

	eval, err := toolpolicy.BuildEvalContext(context.Background(), mgr.Coordinator.Guards.PolicyDependencies(), sess, "read_file", map[string]any{"path": "x"})
	testutil.FailErr(t, "capture tool policy", err)
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

	want := []string{"PolicySnapshot"}
	if len(view.calls) != len(want) {
		t.Fatalf("calls = %v want %v", view.calls, want)
	}
	for i, name := range want {
		if view.calls[i] != name {
			t.Fatalf("call[%d] = %q want %q (all=%v)", i, view.calls[i], name, view.calls)
		}
	}
}

func (s *recordingWorkflowView) RecordReviewToolResult(context.Context, string, api.Message) error {
	s.record("RecordReviewToolResult")
	return nil
}

// Kick obligations for a run on a sealed version come from that version's
// gate feedback, as its other guidance does.
func TestKickGateObligationsFollowTheRunArchive(t *testing.T) {
	gateFeedback, err := feedback.LoadGateFeedbackCatalog()
	testutil.FailErr(t, "load gate feedback", err)
	frame := inject.CoordinatorTurnFrame{}
	frame.RunContext.FailedLeaves = []string{"evidence_passed:survey_challenged"}
	satisfy := func(archive string) string {
		mgr := NewHost(store.NewMemory(), Models{Limits: settings.DefaultSessionLimits()}, nil)
		mgr.SetWorkflowHints(nil, gateFeedback)
		mgr.SetWorkflowDomains(workflowDomainFixture(&recordingWorkflowView{archive: archive}))
		rows := mgr.Coordinator.Guidance.GateObligations(t.Context(), "session", frame)
		if len(rows) != 1 {
			t.Fatalf("obligations = %+v", rows)
		}
		return strings.Join(rows[0].Satisfy, "\n")
	}
	sealed, live := satisfy("security-survey/1.0.0"), satisfy("")
	want := gateFeedback.WithWorkflowArchive("security-survey/1.0.0").ProjectObligations(t.Context(), frame.RunContext.FailedLeaves, "", nil)
	if sealed == live || sealed != strings.Join(want[0].Satisfy, "\n") {
		t.Fatalf("sealed run obligations = %q, live = %q", sealed, live)
	}
}

func (r *recordingWorkflowView) PolicySnapshot(context.Context, string) (toolpolicy.WorkflowSnapshot, error) {
	r.record("PolicySnapshot")
	return toolpolicy.WorkflowSnapshot{Phase: "phase-a", AllowedAgents: []string{"coordinator"}, ManifestRules: []string{"manifest-rules.yaml"}, BlueprintPath: "plan-1", PlanContent: "plan body", Vars: map[string]any{"k": "v"}}, nil
}
