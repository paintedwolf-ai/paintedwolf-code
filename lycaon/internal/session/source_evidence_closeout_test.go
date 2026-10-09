package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"os"
	"path/filepath"
	"testing"
)

func sourceEvidenceCloseoutHarness(t *testing.T) (*Manager, *api.Session, []api.Message) {
	t.Helper()
	mgr, sess := newSynthesisDelayManager(t)
	mgr.SetDataDir(t.TempDir())
	mgr.SetEvidenceStore(inspector.NewJSONLStore(inspector.DefaultEvidenceDir))
	mgr.verificationSource = func(_ context.Context, root string) (string, string) {
		return invocation.SourceRevisionForRoot(root)
	}
	sess.ProjectID = "source-evidence-project"
	sess.WorkspacePath = t.TempDir()
	history := workSince([]api.Message{{Role: api.MessageRoleUser, Content: "change it"}})
	return mgr, sess, history
}

func TestSourceEvidenceCloseoutAllowsRoutineWorkWithoutAssessment(t *testing.T) {
	mgr, sess, history := sourceEvidenceCloseoutHarness(t)
	reject, blocked := mgr.maybeRejectCloseoutForSourceEvidence(
		context.Background(), sess, history, "implement_investigate", true,
	)
	if blocked || reject != nil {
		t.Fatalf("reject=%v blocked=%v", reject, blocked)
	}
}

func TestSourceEvidenceCloseoutAcceptsCurrentPass(t *testing.T) {
	mgr, sess, history := sourceEvidenceCloseoutHarness(t)
	workflowFixture1 := verifyWorkflowStub{required: true}
	mgr.SetWorkflowDomains(&WorkflowDomains{Runs: workflowFixture1, Policy: workflowFixture1, Ambient: workflowFixture1, Blueprints: workflowFixture1, Batch: workflowFixture1, Slash: workflowFixture1, Requests: workflowFixture1, Feedback: workflowFixture1, Transcript: workflowFixture1, Asks: workflowFixture1, Fanout: workflowFixture1, Phases: workflowFixture1, Reports: workflowFixture1, Recovery: workflowFixture1, Cleanup: workflowFixture1})
	recordVerify(t, mgr, sess, "go test ./...", 0)
	if _, blocked := mgr.maybeRejectCloseoutForSourceEvidence(
		context.Background(), sess, history, "implement_investigate", true,
	); blocked {
		t.Fatal("current passing verify should release closeout")
	}
}

func TestSourceEvidenceCloseoutAcceptsCurrentCommand(t *testing.T) {
	mgr, sess, history := sourceEvidenceCloseoutHarness(t)
	workflowFixture2 := verifyWorkflowStub{required: true}
	mgr.SetWorkflowDomains(&WorkflowDomains{Runs: workflowFixture2, Policy: workflowFixture2, Ambient: workflowFixture2, Blueprints: workflowFixture2, Batch: workflowFixture2, Slash: workflowFixture2, Requests: workflowFixture2, Feedback: workflowFixture2, Transcript: workflowFixture2, Asks: workflowFixture2, Fanout: workflowFixture2, Phases: workflowFixture2, Reports: workflowFixture2, Recovery: workflowFixture2, Cleanup: workflowFixture2})
	recordCommand(t, mgr, sess, "./ntp_check.py --json", 0)
	if _, blocked := mgr.maybeRejectCloseoutForSourceEvidence(
		context.Background(), sess, history, "implement_investigate", true,
	); blocked {
		t.Fatal("current passing command should release undeclared closeout")
	}
}

func TestSourceEvidenceCloseoutAllowsExplicitUnverifiedAfterBoundedAttempts(t *testing.T) {
	mgr, sess, history := sourceEvidenceCloseoutHarness(t)
	workflowFixture3 := verifyWorkflowStub{required: true}
	mgr.SetWorkflowDomains(&WorkflowDomains{Runs: workflowFixture3, Policy: workflowFixture3, Ambient: workflowFixture3, Blueprints: workflowFixture3, Batch: workflowFixture3, Slash: workflowFixture3, Requests: workflowFixture3, Feedback: workflowFixture3, Transcript: workflowFixture3, Asks: workflowFixture3, Fanout: workflowFixture3, Phases: workflowFixture3, Reports: workflowFixture3, Recovery: workflowFixture3, Cleanup: workflowFixture3})
	for range maxVerifyAttemptsPerRun {
		recordVerify(t, mgr, sess, "go test ./...", 1)
	}
	if _, blocked := mgr.maybeRejectCloseoutForSourceEvidence(
		context.Background(), sess, history, "implement_investigate", true,
	); blocked {
		t.Fatal("bounded failed attempts should permit a partial, unverified closeout")
	}
}

type sourceEvidenceWorkerQueue struct {
	noopWorkerBranchClaim
	task *api.WorkerTask
}

func (q sourceEvidenceWorkerQueue) Get(jobID string) (*api.WorkerTask, bool) {
	if q.task == nil || q.task.ID != jobID {
		return nil, false
	}
	cp := *q.task
	return &cp, true
}

func (q sourceEvidenceWorkerQueue) ListBySession(context.Context, string, string, ...api.WorkerStatus) ([]api.WorkerTask, error) {
	return nil, nil
}

func workerSourceEvidenceCloseoutHarness(t *testing.T) (*Manager, *api.Session, *api.WorkerTask, []api.Message) {
	t.Helper()
	mgr, parent := newSynthesisDelayManager(t)
	mgr.verificationSource = func(_ context.Context, root string) (string, string) {
		return invocation.SourceRevisionForRoot(root)
	}
	primary := t.TempDir()
	overlay := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-worker")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(overlay, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "a.go"), []byte("package a\n"), 0o644))
	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.go"}}
	snap := testbaseline.Capture(t, primary)
	raw := snap
	child, err := mgr.store.CreateChild(context.Background(), parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "CreateChild", err)
	task := &api.WorkerTask{
		ID: "job-worker", ParentSessionID: parent.ID, ChildSessionID: child.ID,
		WorkspacePath: primary, WorkspaceRoot: overlay, AgentType: "implementer",
		Status: api.WorkerStatusRunning, MergeStatus: api.WorkerMergeStatusPending,
		Scope: &scope, WorkspaceBaselinePath: raw,
	}
	mgr.SetWorkerQueue(sourceEvidenceWorkerQueue{task: task})
	return mgr, child, task, nil
}

func TestWorkerSourceEvidenceCloseoutDoesNotGateOnValidation(t *testing.T) {
	for _, selected := range []string{"", "project-check"} {
		for _, verdict := range []string{"", api.SourceVerdictPassed, api.SourceVerdictFailed} {
			t.Run("selected="+selected+"/verdict="+verdict, func(t *testing.T) {
				mgr, child, task, history := workerSourceEvidenceCloseoutHarness(t)
				mgr.SetVerifyConfig(stubVerifyConfig{cmd: selected})
				workflowFixture4 := verifyWorkflowStub{required: true}
				mgr.SetWorkflowDomains(&WorkflowDomains{Runs: workflowFixture4, Policy: workflowFixture4, Ambient: workflowFixture4, Blueprints: workflowFixture4, Batch: workflowFixture4, Slash: workflowFixture4, Requests: workflowFixture4, Feedback: workflowFixture4, Transcript: workflowFixture4, Asks: workflowFixture4, Fanout: workflowFixture4, Phases: workflowFixture4, Reports: workflowFixture4, Recovery: workflowFixture4, Cleanup: workflowFixture4})
				if verdict != "" {
					history = append(history, api.Message{
						Role: api.MessageRoleTool,
						ToolResult: &api.ToolResult{
							Invocation: &api.InvocationReceipt{
								ID: "receipt-check", Tool: "verify",
								Status: api.InvocationStatusCompleted, SourceVerdict: verdict,
							},
						},
					})
				}
				reject, blocked := mgr.maybeRejectCloseoutForSourceEvidence(
					workercontext.WithJob(t.Context(), task.ID), child, history, "implement_investigate", true,
				)
				if blocked || reject != nil {
					t.Fatalf("worker validation blocked closeout: reject=%v blocked=%v", reject, blocked)
				}
			})
		}
	}
}

func TestWorkerDecisionPauseAllowsHandoff(t *testing.T) {
	mgr, child, task, history := workerSourceEvidenceCloseoutHarness(t)
	ctx := workercontext.WithJob(t.Context(), task.ID)
	if _, blocked := mgr.maybeRejectCloseoutForSourceEvidence(ctx, child, history, "", true); blocked {
		t.Fatal("routine worker closeout must not require validation")
	}
	decisions := NewMemoryDecisionStore()
	mgr.SetDecisionStore(decisions)
	testutil.FailErr(t, "record worker decision", decisions.Put(ctx, api.WorkerDecisionRequest{ChildSessionID: child.ID, WorkerID: task.ID, Question: "Choose the contract", Options: []string{"A", "B"}}))
	reject, blocked := mgr.beforePromptLoopFinish(ctx, child, history, "", "Waiting for a decision", "", true, []string{"request_decision"}, true)
	if blocked || reject != nil {
		t.Fatalf("accepted decision forced worker closeout: blocked=%v reject=%v", blocked, reject)
	}
}

func (sourceEvidenceWorkerQueue) ListPendingOutcomes(context.Context) ([]api.WorkerTask, error) {
	return nil, nil
}
