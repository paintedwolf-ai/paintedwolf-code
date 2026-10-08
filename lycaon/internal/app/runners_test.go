package app

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bootrecovery"
)

func TestStopRunnersDrainsPromptRunners(t *testing.T) {
	app := &ServeApp{}
	app.registerRunner("prompt", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if err := app.startRunners(context.Background()); err != nil {
		t.Fatalf("start runners: %v", err)
	}

	app.stopRunners()

	if names := app.unfinishedRunners(); len(names) != 0 {
		t.Fatalf("unfinished runners = %v, want none", names)
	}
}

// A wedged runner cannot hold the instance lock indefinitely.
func TestStopRunnersBoundsRunnerThatIgnoresCancel(t *testing.T) {
	prev := serveRunnerDrainTimeout
	serveRunnerDrainTimeout = 50 * time.Millisecond
	t.Cleanup(func() { serveRunnerDrainTimeout = prev })

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	app := &ServeApp{}
	app.registerRunner("wedged", func(context.Context) error {
		<-release
		return nil
	})
	if err := app.startRunners(context.Background()); err != nil {
		t.Fatalf("start runners: %v", err)
	}

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		app.stopRunners()
	}()
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("stopRunners blocked on a runner that ignores cancellation")
	}

	if names := app.unfinishedRunners(); !slices.Contains(names, "wedged") {
		t.Fatalf("unfinished runners = %v, want the wedged runner named", names)
	}
}

func TestBackgroundRunnerRestartsAfterFailure(t *testing.T) {
	app := &ServeApp{}
	var attempts atomic.Int32
	second := make(chan struct{})
	app.registerRunner("poller", func(ctx context.Context) error {
		if attempts.Add(1) == 1 {
			return errors.New("database temporarily unavailable")
		}
		close(second)
		<-ctx.Done()
		return ctx.Err()
	})
	app.runners[0].retryDelay = func(int) time.Duration { return time.Millisecond }
	if err := app.startRunners(context.Background()); err != nil {
		t.Fatalf("start runners: %v", err)
	}
	t.Cleanup(app.stopRunners)

	select {
	case <-second:
	case <-time.After(5 * time.Second):
		t.Fatal("runner was not restarted")
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d want 2", got)
	}
}

func TestBackgroundRunnerRestartsAfterPanic(t *testing.T) {
	app := &ServeApp{}
	var attempts atomic.Int32
	restarted := make(chan struct{})
	app.registerRunner("poller", func(ctx context.Context) error {
		if attempts.Add(1) == 1 {
			panic("corrupt poll iteration")
		}
		close(restarted)
		<-ctx.Done()
		return ctx.Err()
	})
	app.runners[0].retryDelay = func(int) time.Duration { return time.Millisecond }
	if err := app.startRunners(context.Background()); err != nil {
		t.Fatalf("start runners: %v", err)
	}
	t.Cleanup(app.stopRunners)

	select {
	case <-restarted:
	case <-time.After(5 * time.Second):
		t.Fatal("runner was not restarted after panic")
	}
}

func TestOneShotRunnerFailureAbortsStartup(t *testing.T) {
	app := &ServeApp{}
	var attempts atomic.Int32
	app.registerOneShotRunner("recovery", func(context.Context) error {
		attempts.Add(1)
		return errors.New("structural recovery failure")
	})
	err := app.startRunners(context.Background())
	if err == nil {
		t.Fatal("expected startup error")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d want 1", got)
	}
	if app.runnersActive {
		t.Fatal("runners remained active after startup failure")
	}
}

func TestReportRecoveryBlocksNormalServing(t *testing.T) {
	boom := errors.New("source mutation journal remained unsettled")
	b := &serveBuilder{}
	err := delegationWiring{b}.reportRecovery(bootrecovery.Report{
		Phase: bootrecovery.PhaseBuild,
		Outcomes: []bootrecovery.Outcome{{
			Name: "source-mutations", Kind: bootrecovery.KindJournal, Phase: bootrecovery.PhaseBuild, Err: boom,
		}},
	}, nil)
	var unsettled *bootrecovery.UnsettledError
	if !errors.As(err, &unsettled) {
		t.Fatalf("reportRecovery error = %v, want UnsettledError", err)
	}
	if len(unsettled.Outcomes) != 1 || !errors.Is(unsettled.Outcomes[0].Err, boom) {
		t.Fatalf("unsettled outcomes = %+v", unsettled.Outcomes)
	}
}

func TestRunnerRegistryRejectsMalformedEntries(t *testing.T) {
	tests := map[string]func(*ServeApp){
		"missing function": func(app *ServeApp) {
			app.registerRunner("poller", nil)
		},
		"duplicate name": func(app *ServeApp) {
			app.registerRunner("poller", func(context.Context) error { return nil })
			app.registerRunner("poller", func(context.Context) error { return nil })
		},
	}
	for name, arrange := range tests {
		t.Run(name, func(t *testing.T) {
			app := &ServeApp{}
			arrange(app)
			if err := app.startRunners(t.Context()); err == nil {
				t.Fatal("expected runner registry error")
			}
			if app.runnersActive {
				t.Fatal("malformed registry started runners")
			}
		})
	}
}
