package loopwake

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

type boardRunFrame struct{ err error }

func (s boardRunFrame) BuildCoordinatorTurnFrame(context.Context, string, *api.Session) (inject.CoordinatorTurnFrame, error) {
	return inject.CoordinatorTurnFrame{RunContext: api.CoordinatorRunContext{RunID: "run-1", CurrentPhase: "work"}}, s.err
}

func TestBoardReinjectionRequiresKnownRun(t *testing.T) {
	for _, tc := range []struct {
		name           string
		source         inject.CoordinatorTurnFrameSource
		wantInform     int
		wantPrediction int
	}{
		{name: "missing frame source", wantInform: 1},
		{name: "failed frame read", source: boardRunFrame{err: errors.New("frame unavailable")}, wantInform: 1},
		{name: "known run", source: boardRunFrame{}, wantPrediction: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := NewLoopEngine()
			sess := &api.Session{ID: "s1", Status: api.SessionStatusBusy, WorkspacePath: t.TempDir()}
			deps := loopDepsForTest()
			deps.GetSession = func(context.Context, string) (*api.Session, error) { return sess, nil }
			deps.WorkflowSource = StubLoopWF{run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "work"}}
			deps.CoordinatorFrame = tc.source
			informed, predicted := 0, 0
			deps.QueueInform = func(context.Context, string, anchor.ID, anchor.Envelope) { informed++ }
			deps.BoardWillForceInject = func(_ context.Context, _ *api.Session, run api.CoordinatorRunContext) bool {
				predicted++
				if run.RunID != "run-1" || run.CurrentPhase != "work" {
					t.Fatalf("board prediction received run %+v", run)
				}
				return true
			}
			engine.SetDeps(deps)
			engine.Nudges.Nudge(t.Context(), sess.ID, anchor.LegFinished, anchor.LegFinished, "leg-1", anchor.Envelope{})
			if informed != tc.wantInform || predicted != tc.wantPrediction {
				t.Fatalf("inform calls=%d prediction calls=%d, want %d and %d", informed, predicted, tc.wantInform, tc.wantPrediction)
			}
		})
	}
}
