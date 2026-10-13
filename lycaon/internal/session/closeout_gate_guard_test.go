package session

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/guidance"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/workflowfacts"
	"github.com/lycaon/lycaon/pkg/api"
)

// gatedCloseoutView reports a gated phase with the given open gate leaves.
type gatedCloseoutView struct {
	stubWorkflowManifest
	state workflowfacts.WorkflowCloseoutGateState
	err   error
}

func (v gatedCloseoutView) ActiveCloseoutGateState(context.Context, string) (workflowfacts.WorkflowCloseoutGateState, error) {
	return v.state, v.err
}

func gatedExecuteState() workflowfacts.WorkflowCloseoutGateState {
	return workflowfacts.WorkflowCloseoutGateState{
		Gated:      true,
		Phase:      "execute",
		OpenLeaves: []string{"worker_cycle_ready"},
	}
}

func TestGatedCloseoutStopsWhenInvokeGated(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(gatedCloseoutView{state: gatedExecuteState()}))

	if reject, block := mgr.Coordinator.Guards.OpenGates(context.Background(), sess, true, false); !block || !errors.Is(reject, guidance.ErrCloseoutStopped) {
		t.Fatal("forced finish must stop with open gates")
	}
}

func TestGatedCloseoutBlocksOnOpenGates(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(gatedCloseoutView{state: gatedExecuteState()}))

	reject, block := mgr.Coordinator.Guards.OpenGates(context.Background(), sess, true, true)
	if !block {
		t.Fatal("expected the closeout to be held while the gated phase's gates are open")
	}
	msg := reject.Error()
	if !strings.Contains(msg, "WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT") {
		t.Fatalf("reject should carry the hint code, got %q", msg)
	}
	if !strings.Contains(msg, "worker_cycle_ready") {
		t.Fatalf("reject should name the open gate leaves, got %q", msg)
	}
	if !strings.Contains(msg, "ask_user") {
		t.Fatalf("reject should name the ask_user exit, got %q", msg)
	}
}

func TestGatedCloseoutAllowsWhenNotGated(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(gatedCloseoutView{state: workflowfacts.WorkflowCloseoutGateState{}}))

	if _, block := mgr.Coordinator.Guards.OpenGates(context.Background(), sess, true, true); block {
		t.Fatal("a phase without a gated closeout must let the prose finish through")
	}
}

func TestGatedCloseoutSkipsBusyWorkers(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(gatedCloseoutView{state: gatedExecuteState()}))

	if _, block := mgr.Coordinator.Guards.OpenGates(context.Background(), sess, false, true); block {
		t.Fatal("the hold must not apply while workers are still in flight")
	}
}

func TestGatedCloseoutBoundedPerPrompt(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(gatedCloseoutView{state: gatedExecuteState()}))
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, block := mgr.Coordinator.Guards.OpenGates(ctx, sess, true, true); !block {
			t.Fatalf("delay %d should still block", i)
		}
	}
	if reject, block := mgr.Coordinator.Guards.OpenGates(ctx, sess, true, true); !block || !errors.Is(reject, guidance.ErrCloseoutStopped) {
		t.Fatal("repair exhaustion must stop without authorizing completion")
	}

	mgr.Runner.Closeouts.BeginPrompt(ctx, sess, promptinput.Input{Text: "continue"})
	if _, block := mgr.Coordinator.Guards.OpenGates(ctx, sess, true, true); !block {
		t.Fatal("a fresh prompt should hold the closeout again")
	}
}

func (v gatedCloseoutView) AssertSessionCompletion(context.Context, string) error {
	if v.err != nil {
		return v.err
	}
	if v.state.Gated {
		return &workflowfacts.IncompleteCloseoutError{State: v.state}
	}
	return nil
}

func TestGatedCloseoutRejectsUnreadableState(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	failure := errors.New("workflow ledger unavailable")
	mgr.SetWorkflowDomains(workflowDomainFixture(gatedCloseoutView{err: failure}))
	reject, block := mgr.Coordinator.Guards.OpenGates(t.Context(), sess, true, true)
	if !block || !errors.Is(reject, guidance.ErrCloseoutStopped) || !errors.Is(reject, failure) {
		t.Fatalf("closeout=%v block=%t", reject, block)
	}
}

func TestPendingOverlayCloseoutUsesLedgerOnEveryTurnOrigin(t *testing.T) {
	for _, origin := range []api.MessageOrigin{api.MessageOriginUser, api.MessageOriginHost} {
		t.Run(string(origin), func(t *testing.T) {
			mgr, sess := newSynthesisDelayManager(t)
			mgr.SetWorkerQueue(jobLister{tasks: []api.WorkerTask{{
				ID: "pending-worker", ParentSessionID: sess.ID, ProjectID: sess.ProjectID,
				Status: api.WorkerStatusComplete, MergeStatus: api.WorkerMergeStatusPending,
				WorkspaceRoot: t.TempDir(), Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite},
			}}})
			history := []api.Message{{Role: api.MessageRoleUser, Origin: origin, Content: "Continue"}}
			if origin == api.MessageOriginHost {
				history[0].Kind = api.MessageKindHostLoopWake
			}
			for _, invokeAllowed := range []bool{true, false} {
				reject, blocked := mgr.Coordinator.Guards.BeforeFinish(t.Context(), sess, history, "", "Finished", "implement_overlay_promote", true, []string{"read"}, invokeAllowed)
				if !blocked || reject == nil || reject.Code() != "OVERLAY_DISPOSITION_REQUIRED_BEFORE_CLOSEOUT" {
					t.Fatalf("invoke=%t closeout=%v blocked=%t", invokeAllowed, reject, blocked)
				}
				if errors.Is(reject, guidance.ErrCloseoutStopped) == invokeAllowed {
					t.Fatalf("invoke=%t stop=%v", invokeAllowed, reject)
				}
				if !strings.Contains(reject.Body, `promote_overlay(overlay_id="pending-worker")`) {
					t.Fatalf("recovery omitted the actual overlay: %s", reject.Body)
				}
			}
			if err := mgr.Coordinator.Guards.AssertCompletion(t.Context(), sess); !errors.Is(err, guidance.ErrCloseoutStopped) {
				t.Fatalf("host-assembled completion bypassed pending delivery: %v", err)
			}
		})
	}
}

func TestPendingOverlayCloseoutClearsOnlyAfterDisposition(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	task := api.WorkerTask{ID: "worker", ParentSessionID: sess.ID, ProjectID: sess.ProjectID,
		Status: api.WorkerStatusComplete, MergeStatus: api.WorkerMergeStatusPending,
		WorkspaceRoot: t.TempDir(), Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite}}
	mgr.SetWorkerQueue(jobLister{tasks: []api.WorkerTask{task}})
	if _, blocked := mgr.Coordinator.Guards.PendingOverlays(t.Context(), sess, true); !blocked {
		t.Fatal("pending overlay did not block")
	}
	for _, status := range []api.WorkerMergeStatus{api.WorkerMergeStatusMerged, api.WorkerMergeStatusRejected} {
		task.MergeStatus = status
		mgr.SetWorkerQueue(jobLister{tasks: []api.WorkerTask{task}})
		if reject, blocked := mgr.Coordinator.Guards.PendingOverlays(t.Context(), sess, true); blocked {
			t.Fatalf("settled %s overlay blocked: %v", status, reject)
		}
	}
	task.MergeStatus = api.WorkerMergeStatusPending
	task.Scope = &api.TaskScope{Mode: api.TaskScopeModeRead}
	mgr.SetWorkerQueue(jobLister{tasks: []api.WorkerTask{task}})
	if _, blocked := mgr.Coordinator.Guards.PendingOverlays(t.Context(), sess, true); blocked {
		t.Fatal("read worker created an overlay obligation")
	}
}
