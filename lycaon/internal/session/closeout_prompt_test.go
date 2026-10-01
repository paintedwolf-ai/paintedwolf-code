package session

import (
	"errors"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPromptExecutionAppliesCloseoutBoundaryBeforeModel(t *testing.T) {
	for _, host := range []bool{false, true} {
		name := "new intent"
		if host {
			name = "host continuation"
		}
		t.Run(name, func(t *testing.T) {
			mgr, sessions := newTestManager(t)
			stopped := &failure.ProviderEmptyCompletionError{ProviderID: "fixture"}
			mgr.llm = failingTurnClient{failure: stopped}
			sess, err := sessions.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create closeout session", err)
			seedCloseoutState(&mgr.closeout, sess.ID, sess.ID)
			_, wantCycle := closeoutState(&mgr.closeout, sess.ID, sess.ID)
			if host {
				_, err = mgr.promptHostLoopWake(t.Context(), sess.ID)
			} else {
				wantCycle = closeoutCycleState{}
				_, err = mgr.Prompt(t.Context(), sess.ID, "New direction")
			}
			if !errors.Is(err, stopped) {
				t.Fatalf("prompt did not reach model: %v", err)
			}
			prompt, cycle := closeoutState(&mgr.closeout, sess.ID, sess.ID)
			if prompt != (closeoutPromptState{}) || !reflect.DeepEqual(cycle, wantCycle) {
				t.Fatalf("closeout boundary = %+v / %+v; want empty prompt and %+v", prompt, cycle, wantCycle)
			}
		})
	}
}

func TestCloseoutClearStallPreservesBudgetsAndSnapshotIsolation(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	seedCloseoutState(&mgr.closeout, sess.ID, sess.ID)
	beforePrompt, beforeCycle := closeoutState(&mgr.closeout, sess.ID, sess.ID)
	codes := mgr.CloseoutStallState(t.Context(), sess.ID).ForcedBy
	codes[0] = "caller mutation"
	if got := mgr.closeout.stallState(sess.ID).ForcedBy[0]; got != "citation" {
		t.Fatalf("snapshot changed retained state: %q", got)
	}
	mgr.ClearCloseoutStall(t.Context(), sess.ID)
	prompt, cycle := closeoutState(&mgr.closeout, sess.ID, sess.ID)
	beforeCycle.stall = closeoutStall{}
	if prompt != beforePrompt || !reflect.DeepEqual(cycle, beforeCycle) {
		t.Fatalf("commit changed budgets or retained draft state: %+v / %+v", prompt, cycle)
	}
}

func TestCloseoutChildRewindClearsRootAndChild(t *testing.T) {
	m := &Manager{}
	seedCloseoutState(&m.closeout, "root", "root")
	seedCloseoutState(&m.closeout, "child", "root")
	m.rollbackTurnLedgers("child", "root")
	for _, id := range []string{"root", "child"} {
		prompt, cycle := closeoutState(&m.closeout, id, "root")
		if prompt != (closeoutPromptState{}) || !reflect.DeepEqual(cycle, closeoutCycleState{}) {
			t.Fatalf("rewind retained %s: %+v / %+v", id, prompt, cycle)
		}
	}
}
