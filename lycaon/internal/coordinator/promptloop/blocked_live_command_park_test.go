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
