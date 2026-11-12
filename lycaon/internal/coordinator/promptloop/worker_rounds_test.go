package promptloop

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// A spent worker round ends before its next model call, even when a soft stop
// would give a coordinator one more round.
func TestApplySpendCeilingFencesWorkerRound(t *testing.T) {
	ceilingErr := errors.New("ceiling reached")
	loop := NewPromptLoop(PromptLoopDeps{
		Nudges: NudgesDeps{
			CheckSpendCeiling: func(context.Context, string, *api.Session) (SpendCeilingCheck, error) {
				return SpendCeilingCheck{SoftStop: true}, ceilingErr
			},
			IsSpendCeiling: func(err error) bool { return errors.Is(err, ceilingErr) },
		},
	})
	st := &promptLoopTurnState{turnStreamState: turnStreamState{lastAssistantID: "a1"}}
	worker := &api.Session{ID: "child", ParentSessionID: "parent"}
	decision, err := loop.Nudges.applySpendCeiling(context.Background(), worker, "child", "implement", "prompt", 10, PromptRunInput{}, st)
	if err != nil {
		t.Fatalf("applySpendCeiling: %v", err)
	}
	if decision.WindDown || st.spendSoftStopGranted {
		t.Fatalf("worker got a wind-down round: decision=%+v", decision)
	}
	if decision.Finished == nil || decision.Finished.Outcome != promptresult.OutcomeSpendLimit || !st.turnEndedGuidanceReject {
		t.Fatalf("worker round not fenced: %+v", decision.Finished)
	}
}

func TestConsumptionNoticeAtThresholdAndEachDoubling(t *testing.T) {
	const base = 5
	var notified []int
	loop := NewPromptLoop(PromptLoopDeps{
		Nudges: NudgesDeps{
			WorkerRounds: spawn.RoundPolicy{ConsumptionNoticeModelCalls: base},
			WorkerRoundConsumption: func(_ context.Context, _ *api.Session, jobID string, modelCalls int) {
				if jobID != "job-1" {
					t.Fatalf("notice for job %q", jobID)
				}
				notified = append(notified, modelCalls)
			},
		},
	})
	sess := &api.Session{ID: "child", ParentSessionID: "parent"}
	for calls := 1; calls <= base*8; calls++ {
		loop.Nudges.maybeConsumptionNotice(context.Background(), sess, "job-1", calls)
	}
	if want := []int{base, base * 2, base * 4, base * 8}; !slices.Equal(notified, want) {
		t.Fatalf("notices at %v, want %v", notified, want)
	}

	notified = nil
	loop.Nudges.maybeConsumptionNotice(context.Background(), sess, " ", base)
	if len(notified) != 0 {
		t.Fatalf("notice without a job: %v", notified)
	}
}

func TestToollessResponseBoundFallsBackToDefault(t *testing.T) {
	loop := NewPromptLoop(PromptLoopDeps{})
	if got, want := loop.Nudges.toollessResponseBound(), spawn.DefaultRoundPolicy().ToollessResponseBound; got != want {
		t.Fatalf("bound = %d, want %d", got, want)
	}
	loop = NewPromptLoop(PromptLoopDeps{Nudges: NudgesDeps{WorkerRounds: spawn.RoundPolicy{ToollessResponseBound: 2}}})
	if got := loop.Nudges.toollessResponseBound(); got != 2 {
		t.Fatalf("bound = %d, want 2", got)
	}
}

func workerReadCall(id string) scriptedModelStep {
	return scriptedModelStep{completion: &modelcall.Completion{
		ToolCalls: []api.ToolCall{{ID: id, Name: "command", Args: map[string]any{"command": "cat a.txt"}}},
	}}
}

func workerText(text string) scriptedModelStep {
	return scriptedModelStep{completion: &modelcall.Completion{Content: text}}
}

// A worker round has no round ceiling: it keeps calling the model past the
// session's iteration limit and ends after its bound of consecutive tool-less
// responses. The scripted model answers tool-less once its script runs out.
func TestWorkerRoundEndsOnlyAtToollessBound(t *testing.T) {
	const maxIter = 2
	bound := spawn.DefaultRoundPolicy().ToollessResponseBound
	steps := []scriptedModelStep{workerReadCall("r1"), workerReadCall("r2"), workerReadCall("r3"), workerReadCall("r4")}
	h, client := newAttemptHarness(t, true, maxIter, steps)
	testutil.FailErr(t, "run worker round", h.runAttempt(t, "Survey the package.", "implementer", "job-1"))
	if client.index != len(steps) {
		t.Fatalf("model consumed %d scripted steps, want %d", client.index, len(steps))
	}
	h.assertStrictlyIncreasingIterations(t, len(steps)+bound)
}

// Only consecutive tool-less responses count toward the bound.
func TestWorkerToolCallResetsToollessCount(t *testing.T) {
	bound := spawn.DefaultRoundPolicy().ToollessResponseBound
	var steps []scriptedModelStep
	for i := range 2 {
		for range bound - 1 {
			steps = append(steps, workerText("thinking aloud"))
		}
		steps = append(steps, workerReadCall("read-"+string(rune('a'+i))))
	}
	h, client := newAttemptHarness(t, true, 50, steps)
	testutil.FailErr(t, "run worker round", h.runAttempt(t, "Survey the package.", "implementer", "job-1"))
	if client.index != len(steps) {
		t.Fatalf("round ended after %d of %d scripted steps", client.index, len(steps))
	}
	h.assertStrictlyIncreasingIterations(t, len(steps)+bound)
}
