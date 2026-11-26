package loopwake

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

type outcomeFixture struct {
	loopEnabled bool
	actionable  bool
	turnIDs     []string
}

func (f *outcomeFixture) loop() *LoopEngine {
	loop := NewLoopEngine()
	loop.SetDeps(LoopDeps{
		GetSession: func(_ context.Context, id string) (*api.Session, error) {
			return &api.Session{ID: id, Status: api.SessionStatusIdle}, nil
		},
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
		Limits: func(context.Context, *api.Session) settings.SessionLimits {
			limits := settings.DefaultSessionLimits()
			enabled := f.loopEnabled
			limits.CoordinatorLoop = &enabled
			return limits
		},
		WorkerCycleIdle:    func(context.Context, *api.Session, string) (bool, error) { return true, nil },
		WorkflowSource:     workflowFixturePorts(activeLoopWorkflow{}),
		HostWakeActionable: func(context.Context, HostWakeActionableInput) bool { return f.actionable },
		RunPrompt: func(ctx context.Context, _ string) (*promptresult.Result, error) {
			f.turnIDs = append(f.turnIDs, LoopWakeTurn(ctx))
			return &promptresult.Result{}, nil
		},
	})
	return loop
}

// requireOneOutcomeEach fails unless every wake in the ledger ended exactly once
// and no decision broke the one-outcome rule.
func requireOneOutcomeEach(t *testing.T, loop *LoopEngine, sessionID string) []WakeOutcome {
	t.Helper()
	var terminal []WakeOutcome
	counts := map[uint64]int{}
	for _, r := range loop.Nudges.Outcomes.Ledger(sessionID) {
		if r.Turn == "" {
			terminal = append(terminal, r)
			counts[r.Seq]++
		}
	}
	for seq, n := range counts {
		if n != 1 {
			t.Fatalf("wake #%d has %d terminal outcomes, want 1", seq, n)
		}
	}
	if v := loop.Nudges.Outcomes.Violations(); v != 0 {
		t.Fatalf("%d one-outcome violations", v)
	}
	return terminal
}

func TestEachWakeDecisionPathRecordsOneOutcome(t *testing.T) {
	for _, tc := range []struct {
		name       string
		fixture    outcomeFixture
		drive      func(*LoopEngine)
		wantResult WakeResult
		wantReason string
		wantTurn   bool
	}{{
		name:    "delivered to a turn",
		fixture: outcomeFixture{loopEnabled: true, actionable: true},
		drive: func(l *LoopEngine) {
			l.Nudges.Nudge(t.Context(), "sess", anchor.PhaseAdvanced, "", "", anchor.Envelope{})
		},
		wantResult: WakeDelivered, wantReason: "run_prompt", wantTurn: true,
	}, {
		name:    "parked when the loop is off",
		fixture: outcomeFixture{actionable: true},
		drive: func(l *LoopEngine) {
			l.Nudges.Nudge(t.Context(), "sess", anchor.PhaseAdvanced, "", "", anchor.Envelope{})
		},
		wantResult: WakeParked, wantReason: "denied",
	}, {
		name:    "parked when not actionable",
		fixture: outcomeFixture{loopEnabled: true},
		drive: func(l *LoopEngine) {
			l.Nudges.Nudge(t.Context(), "sess", anchor.PhaseAdvanced, "", "", anchor.Envelope{})
		},
		wantResult: WakeParked, wantReason: "non_actionable",
	}, {
		name:    "dropped timer during a running prompt",
		fixture: outcomeFixture{loopEnabled: true, actionable: true},
		drive: func(l *LoopEngine) {
			finish := l.Admission.BeginPromptExecution(t.Context(), "sess")
			l.Nudges.Nudge(t.Context(), "sess", anchor.WaitTimerFired, anchor.WaitTimerFired, "", anchor.Envelope{})
			finish()
		},
		wantResult: WakeDropped, wantReason: "precedence_busy",
	}, {
		name:    "queued behind a running prompt, then delivered",
		fixture: outcomeFixture{loopEnabled: true, actionable: true},
		drive: func(l *LoopEngine) {
			finish := l.Admission.BeginPromptExecution(t.Context(), "sess")
			l.Nudges.Nudge(t.Context(), "sess", anchor.PhaseAdvanced, "", "", anchor.Envelope{})
			finish()
		},
		wantResult: WakeDelivered, wantReason: "run_prompt", wantTurn: true,
	}, {
		name:    "queued, then cleared",
		fixture: outcomeFixture{loopEnabled: true, actionable: true},
		drive: func(l *LoopEngine) {
			finish := l.Admission.BeginPromptExecution(t.Context(), "sess")
			l.Nudges.Nudge(t.Context(), "sess", anchor.PhaseAdvanced, "", "", anchor.Envelope{})
			l.Nudges.ClearPending("sess")
			finish()
		},
		wantResult: WakeDropped, wantReason: "cleared",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.fixture
			loop := f.loop()
			tc.drive(loop)
			loop.Turns.WaitForAsyncTurns(t.Context())
			terminal := requireOneOutcomeEach(t, loop, "sess")
			if len(terminal) != 1 {
				t.Fatalf("terminal outcomes = %+v, want one", terminal)
			}
			got := terminal[0]
			if got.Result != tc.wantResult || got.Reason != tc.wantReason {
				t.Fatalf("outcome = %s: %s, want %s: %s", got.Result, got.Reason, tc.wantResult, tc.wantReason)
			}
			if !tc.wantTurn {
				if got.TurnID != "" || len(f.turnIDs) != 0 {
					t.Fatalf("an undelivered wake opened turn %q (turns run %v)", got.TurnID, f.turnIDs)
				}
				return
			}
			if got.TurnID == "" || len(f.turnIDs) != 1 || f.turnIDs[0] != got.TurnID {
				t.Fatalf("delivered turn %q, turns run %v: the turn must run under the identity its wake recorded", got.TurnID, f.turnIDs)
			}
			ledger := loop.Nudges.Outcomes.Ledger("sess")
			if last := ledger[len(ledger)-1]; last.TurnID != got.TurnID || last.Turn != TurnCompleted {
				t.Fatalf("turn record = %+v, want turn %s completed", last, got.TurnID)
			}
		})
	}
}

// Subscribers receive each wake's outcome, then its turn's, in order.
func TestWakeSubscriptionDeliversOutcomesInOrder(t *testing.T) {
	f := outcomeFixture{loopEnabled: true, actionable: true}
	loop := f.loop()
	sub := loop.Nudges.Outcomes.Subscribe("sess")
	defer sub.Close()
	loop.Nudges.Nudge(t.Context(), "sess", anchor.PhaseAdvanced, "", "", anchor.Envelope{})
	delivered, ok := sub.Next(t.Context())
	if !ok || delivered.Result != WakeDelivered || delivered.Turn != "" {
		t.Fatalf("first record = %+v, want the delivered wake", delivered)
	}
	turn, ok := sub.Next(t.Context())
	if !ok || turn.TurnID != delivered.TurnID || turn.Turn != TurnCompleted {
		t.Fatalf("second record = %+v, want turn %s completed", turn, delivered.TurnID)
	}
}

func TestWakeDecisionGuardEnforcesOneOutcome(t *testing.T) {
	outcomes := newWakeOutcomes()
	undecided := pendingLoopWake{wake: anchor.PhaseAdvanced, seq: 1}
	outcomes.opened("sess", undecided.seq)
	func() {
		d := outcomes.decide("sess", undecided)
		defer d.close()
	}()
	twice := pendingLoopWake{wake: anchor.PhaseAdvanced, seq: 2}
	outcomes.opened("sess", twice.seq)
	func() {
		d := outcomes.decide("sess", twice)
		defer d.close()
		d.settle(WakeParked, "denied")
		d.settle(WakeDropped, "cleared")
	}()
	ledger := outcomes.Ledger("sess")
	if len(ledger) != 2 || ledger[0].Result != WakeDropped || ledger[0].Reason != "undecided" ||
		ledger[1].Result != WakeParked || ledger[1].Reason != "denied" {
		t.Fatalf("ledger = %+v, want undecided dropped then the first decision only", ledger)
	}
	if got := outcomes.Violations(); got != 2 {
		t.Fatalf("violations = %d, want 2", got)
	}
	outcomes.settle("sess", twice, WakeDropped, "late", "")
	if got := outcomes.Violations(); got != 3 || len(outcomes.Ledger("sess")) != 2 {
		t.Fatalf("settling a closed wake: violations %d, ledger %d", got, len(outcomes.Ledger("sess")))
	}
}
