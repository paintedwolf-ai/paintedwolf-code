package app

import (
	"context"
	"testing"
	"time"
)

// The shell shutdown budget includes every graceful phase before forced termination.
func TestShutdownBudgetSecondsMatchesTheDrainPhases(t *testing.T) {
	t.Parallel()
	if got := time.Duration(ShutdownBudgetSeconds) * time.Second; got != ShutdownBudget {
		t.Fatalf("ShutdownBudgetSeconds states %v but the phases sum to %v; "+
			"the shell sizes its graceful-stop wait from the declaration", got, ShutdownBudget)
	}
}

// Runner draining fits within the HTTP drain budget.
func TestRunnerDrainFitsInsideTheServeDrain(t *testing.T) {
	t.Parallel()
	if serveRunnerDrainTimeout > serveDrainTimeout {
		t.Fatalf("runner drain %v exceeds the serve drain %v", serveRunnerDrainTimeout, serveDrainTimeout)
	}
}

// The serve loop drains runners under the same deadline it gives the HTTP
// shutdown, so an unresponsive runner cannot push the engine past the budget.
func TestStopRunnersWithinHonorsTheCallerDeadline(t *testing.T) {
	app := &ServeApp{}
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	app.registerRunner("stubborn", func(context.Context) error {
		<-release
		return context.Canceled
	})
	if err := app.startRunners(context.Background()); err != nil {
		t.Fatalf("start runners: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	app.stopRunnersWithin(ctx)
	waited := time.Since(started)

	if waited >= serveRunnerDrainTimeout {
		t.Fatalf("runner drain waited %v; the caller deadline of 100ms must win", waited)
	}
	if names := app.unfinishedRunners(); len(names) != 1 {
		t.Fatalf("unfinished runners = %v, want the stubborn runner named", names)
	}
}
