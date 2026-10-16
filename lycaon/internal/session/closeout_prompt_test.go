package session

import (
	"errors"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
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
			mgr.Coordinator.Model.LLM = failingTurnClient{failure: stopped}
			sess, err := sessions.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create closeout session", err)
			mgr.Runner.Closeouts.NoteCloseoutGroundingReject(t.Context(), sess.ID, "citation", "offender", "draft", nil)
			want := guidance.RetainedCloseout{}
			if host {
				want = guidance.RetainedCloseout{Attempt: 1}
				_, err = mgr.Submissions.LoopWake(t.Context(), sess.ID)
			} else {
				_, err = mgr.Submissions.Prompt(t.Context(), sess.ID, "New direction")
			}
			if !errors.Is(err, stopped) {
				t.Fatalf("prompt did not reach model: %v", err)
			}
			if got := mgr.Runner.Closeouts.CloseoutStallState(t.Context(), sess.ID); !reflect.DeepEqual(got, want) {
				t.Fatalf("closeout boundary = %+v want %+v", got, want)
			}
		})
	}
}
