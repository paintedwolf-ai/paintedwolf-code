package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/invocation"
	sessiondecisions "github.com/lycaon/lycaon/internal/session/decisions"
	"github.com/lycaon/lycaon/internal/session/verification"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func sourceEvidenceCloseoutHarness(t *testing.T) (*Host, *api.Session, []api.Message) {
	t.Helper()
	mgr, sess := newSynthesisDelayManager(t)
	mgr.SetDataDir(t.TempDir())
	mgr.Verification.SetEvidenceStore(inspector.NewJSONLStore(inspector.DefaultEvidenceDir))
	mgr.Verification.SetRevisionSource(func(_ context.Context, root string) (string, string) {
		return invocation.SourceRevisionForRoot(root)
	})
	sess.ProjectID = "source-evidence-project"
	sess.WorkspacePath = t.TempDir()
	history := workSince([]api.Message{{Role: api.MessageRoleUser, Content: "change it"}})
	return mgr, sess, history
}

func TestSourceEvidenceCloseoutAllowsRoutineWorkWithoutAssessment(t *testing.T) {
	mgr, sess, history := sourceEvidenceCloseoutHarness(t)
	reject, blocked := mgr.Coordinator.Guards.SourceEvidence(
		context.Background(), sess, history, "implement_investigate", true,
	)
	if blocked || reject != nil {
		t.Fatalf("reject=%v blocked=%v", reject, blocked)
	}
}

func TestSourceEvidenceCloseoutAcceptsCurrentPass(t *testing.T) {
	mgr, sess, history := sourceEvidenceCloseoutHarness(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(verifyWorkflowStub{required: true}))
	recordVerify(t, mgr, sess, "go test ./...", 0)
	if _, blocked := mgr.Coordinator.Guards.SourceEvidence(
		context.Background(), sess, history, "implement_investigate", true,
	); blocked {
		t.Fatal("current passing verify should release closeout")
	}
}

func TestSourceEvidenceCloseoutAcceptsCurrentCommand(t *testing.T) {
	mgr, sess, history := sourceEvidenceCloseoutHarness(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(verifyWorkflowStub{required: true}))
	recordCommand(t, mgr, sess, "./ntp_check.py --json", 0)
	if _, blocked := mgr.Coordinator.Guards.SourceEvidence(
		context.Background(), sess, history, "implement_investigate", true,
	); blocked {
		t.Fatal("current passing command should release undeclared closeout")
	}
}

func TestSourceEvidenceCloseoutAllowsExplicitUnverifiedAfterBoundedAttempts(t *testing.T) {
	mgr, sess, history := sourceEvidenceCloseoutHarness(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(verifyWorkflowStub{required: true}))
	for range verification.MaxAttemptsPerRun {
		recordVerify(t, mgr, sess, "go test ./...", 1)
	}
	if _, blocked := mgr.Coordinator.Guards.SourceEvidence(
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

func workerSourceEvidenceCloseoutHarness(t *testing.T) (*Host, *api.Session, *api.WorkerTask, []api.Message) {
	t.Helper()
	mgr, parent := newSynthesisDelayManager(t)
	mgr.Verification.SetRevisionSource(func(_ context.Context, root string) (string, string) {
		return invocation.SourceRevisionForRoot(root)
	})
	primary := t.TempDir()
	overlay := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-worker")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(overlay, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "a.go"), []byte("package a\n"), 0o644))
	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.go"}}
	snap := testbaseline.Capture(t, primary)
	raw := snap
	child, err := mgr.Coordinator.Context.Sessions.(Store).CreateChild(context.Background(), parent, api.SpawnChildRequest{AgentType: "implementer"})
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
				mgr.Verification.SetVerifyConfig(stubVerifyConfig{cmd: selected})
				mgr.SetWorkflowDomains(workflowDomainFixture(verifyWorkflowStub{required: true}))
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
				reject, blocked := mgr.Coordinator.Guards.SourceEvidence(
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
	if _, blocked := mgr.Coordinator.Guards.SourceEvidence(ctx, child, history, "", true); blocked {
		t.Fatal("routine worker closeout must not require validation")
	}
	decisions := sessiondecisions.NewMemory()
	mgr.SetDecisionStore(decisions)
	testutil.FailErr(t, "record worker decision", decisions.Put(ctx, api.WorkerDecisionRequest{ChildSessionID: child.ID, WorkerID: task.ID, Question: "Choose the contract", Options: []string{"A", "B"}}))
	reject, blocked := mgr.Coordinator.Guards.BeforeFinish(ctx, child, history, "", "Waiting for a decision", "", true, []string{"request_decision"}, true)
	if blocked || reject != nil {
		t.Fatalf("accepted decision forced worker closeout: blocked=%v reject=%v", blocked, reject)
	}
}

func (sourceEvidenceWorkerQueue) ListPendingOutcomes(context.Context) ([]api.WorkerTask, error) {
	return nil, nil
}
