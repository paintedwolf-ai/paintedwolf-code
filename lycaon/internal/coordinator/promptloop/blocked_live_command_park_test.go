package promptloop

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBlockedLoopParksLiveCommandInsteadOfForcingCloseout(t *testing.T) {
	parked := 0
	closeoutCalls := 0
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Control: ControlDeps{
			ParkBlockedLiveCommands: func(_ context.Context, sessionID string) bool {
				if sessionID != "session-1" {
					t.Fatalf("park session = %q", sessionID)
				}
				parked++
				return true
			},
		},
		Closeout: CloseoutDeps{
			TurnCloseoutNudge: func(context.Context, *api.Session, string, TurnCloseoutCause) HostNudge {
				closeoutCalls++
				return HostNudge{Content: "must not run"}
			},
		},
	})
	st := &promptLoopTurnState{
		lastAssistantID:      "draft-1",
		lastAssistantContent: "the command is still running",
		turnsRanThisRun:      8,
	}

	result, err := loop.finalizePromptLoopRun(
		context.Background(),
		&api.Session{ID: "session-1"},
		"session-1", "coordinator", "build it", 16, false,
		loopExitBlockedLoop, PromptRunInput{}, st,
	)
	testutil.FailErr(t, "finalize blocked loop", err)
	if parked != 1 {
		t.Fatalf("park calls = %d want 1", parked)
	}
	if closeoutCalls != 0 {
		t.Fatalf("forced closeout calls = %d want 0", closeoutCalls)
	}
	if result.LastAssistantID != "" || result.LastAssistantContent != "" {
		t.Fatalf("parked result exposed stale draft: %+v", result)
	}
	if !st.turnEndedGuidanceReject {
		t.Fatal("parked blocked turn must end as a handled guidance rejection")
	}
}

func TestFinalizePromptLoopRunFlushesCloseoutPolicyFeedback(t *testing.T) {
	t.Parallel()
	taken := 0
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Inbox: InboxDeps{
			TakePolicyFeedback: func(_ context.Context, sessionID string) ([]api.Message, error) {
				if sessionID != "session-closeout" {
					t.Fatalf("unexpected sessionID %q", sessionID)
				}
				taken++
				return []api.Message{{ID: "pf-1", Role: api.MessageRoleUser, HostSignalID: "policy_feedback", Content: "Code: COMPLETION_REPORT_UNGROUNDED"}}, nil
			},
		},
	})
	st := &promptLoopTurnState{
		lastAssistantID:      "assistant-1",
		lastAssistantContent: "done",
		turnsRanThisRun:      1,
	}
	res, err := loop.finalizePromptLoopRun(
		context.Background(),
		&api.Session{ID: "session-closeout"},
		"session-closeout", "coordinator", "ship it", 16, false,
		loopExitCompleted, PromptRunInput{}, st,
	)
	testutil.FailErr(t, "finalize completed loop", err)
	if taken != 1 {
		t.Fatalf("TakePolicyFeedback calls = %d, want 1", taken)
	}
	if len(st.history) != 1 || st.history[0].ID != "pf-1" {
		t.Fatalf("expected policy feedback appended to closeout history, got %+v", st.history)
	}
	if res.LastAssistantID != "assistant-1" {
		t.Fatalf("unexpected result: %+v", res)
	}
}
