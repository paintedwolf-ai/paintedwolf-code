package wiring

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/app"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
)

// WakeWatch receives a session's host-wake outcomes from the moment it is
// opened. Open it before the action that causes the wake.
type WakeWatch struct {
	host      *app.ServeApp
	sessionID string
	sub       *loopwake.WakeSubscription
}

// WatchWakes subscribes to the session's wake outcomes until the test ends.
func WatchWakes(t testing.TB, host *app.ServeApp, sessionID string) *WakeWatch {
	t.Helper()
	sub := host.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop().Nudges.Outcomes.Subscribe(sessionID)
	t.Cleanup(sub.Close)
	return &WakeWatch{host: host, sessionID: sessionID, sub: sub}
}

// AwaitOutcome returns the terminal outcome of the next wake of kind wake; an
// empty kind matches any wake. It waits as long as the test runs.
func (w *WakeWatch) AwaitOutcome(t testing.TB, step string, wake anchor.ID) loopwake.WakeOutcome {
	t.Helper()
	for {
		record, ok := w.sub.Next(t.Context())
		if !ok {
			t.Fatalf("%s: the test ended before the wake reported an outcome\n%s", step, w.Report())
		}
		if record.Turn == "" && (wake == "" || record.Wake == wake) {
			return record
		}
	}
}

// AwaitDelivered returns the next wake of kind wake and fails at once, with the
// session's wake ledger, unless it opened a turn.
func (w *WakeWatch) AwaitDelivered(t testing.TB, step string, wake anchor.ID) loopwake.WakeOutcome {
	t.Helper()
	outcome := w.AwaitOutcome(t, step, wake)
	if outcome.Result != loopwake.WakeDelivered {
		t.Fatalf("%s: %s %s: %s\n%s", step, outcome.Wake, outcome.Result, outcome.Reason, w.Report())
	}
	return outcome
}

// AwaitTurn returns how the turn a delivered wake opened ended.
func (w *WakeWatch) AwaitTurn(t testing.TB, step string, delivered loopwake.WakeOutcome) loopwake.WakeOutcome {
	t.Helper()
	for {
		record, ok := w.sub.Next(t.Context())
		if !ok {
			t.Fatalf("%s: the test ended before turn %s reported an outcome\n%s", step, delivered.TurnID, w.Report())
		}
		if record.Turn != "" && record.TurnID == delivered.TurnID {
			return record
		}
	}
}

// AwaitCompletedTurn awaits a delivered wake of kind wake and its turn, failing
// unless the turn completed.
func (w *WakeWatch) AwaitCompletedTurn(t testing.TB, step string, wake anchor.ID) loopwake.WakeOutcome {
	t.Helper()
	turn := w.AwaitTurn(t, step, w.AwaitDelivered(t, step, wake))
	if turn.Turn != loopwake.TurnCompleted {
		t.Fatalf("%s: turn %s %s: %s\n%s", step, turn.TurnID, turn.Turn, turn.TurnWhy, w.Report())
	}
	return turn
}

// Report lists the session's recent wake outcomes, oldest first.
func (w *WakeWatch) Report() string {
	ledger := w.host.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop().Nudges.Outcomes.Ledger(w.sessionID)
	var b strings.Builder
	fmt.Fprintf(&b, "session %s wake outcomes, oldest first:", w.sessionID)
	if len(ledger) == 0 {
		b.WriteString(" none")
	}
	for _, r := range ledger {
		fmt.Fprintf(&b, "\n  #%d %s: %s: %s", r.Seq, r.Wake, r.Result, r.Reason)
		if r.TurnID != "" {
			fmt.Fprintf(&b, " turn %s", r.TurnID)
		}
		if r.Turn != "" {
			fmt.Fprintf(&b, " ended %s", r.Turn)
			if r.TurnWhy != "" {
				fmt.Fprintf(&b, " (%s)", r.TurnWhy)
			}
		}
	}
	return b.String()
}
