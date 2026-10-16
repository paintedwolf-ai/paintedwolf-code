package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/app/security"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBackgroundOneShotDoesNotGateStartupAndJoinsOnCancellation(t *testing.T) {
	app := &ServeApp{}
	entered := make(chan struct{})
	returned := make(chan struct{})
	var calls atomic.Int32
	app.registerBackgroundOneShotRunner("warm", func(ctx context.Context) error {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		close(returned)
		return ctx.Err()
	})
	started := make(chan error, 1)
	go func() { started <- app.startRunners(t.Context()) }()
	select {
	case err := <-started:
		testutil.FailErr(t, "start with blocked warm", err)
	case <-time.After(5 * time.Second):
		t.Fatal("background warm blocked startup")
	}
	t.Cleanup(app.stopRunners)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("background warm never started")
	}
	app.stopRunners()
	select {
	case <-returned:
	default:
		t.Fatal("shutdown returned before warm cancellation completed")
	}
	if len(app.unfinishedRunners()) != 0 || calls.Load() != 1 {
		t.Fatalf("unfinished=%v calls=%d", app.unfinishedRunners(), calls.Load())
	}
}

func TestBackgroundOneShotSettlesOnceForEveryOutcome(t *testing.T) {
	for _, outcome := range []string{"success", "error", "panic"} {
		t.Run(outcome, func(t *testing.T) {
			app := &ServeApp{}
			var calls atomic.Int32
			app.registerBackgroundOneShotRunner("warm", func(context.Context) error {
				calls.Add(1)
				switch outcome {
				case "error":
					return errors.New("fixture failed")
				case "panic":
					panic("fixture panicked")
				default:
					return nil
				}
			})
			testutil.FailErr(t, "start background one shot", app.startRunners(t.Context()))
			t.Cleanup(app.stopRunners)
			select {
			case <-app.runners.runners[0].done:
			case <-time.After(5 * time.Second):
				t.Fatal("one-shot accounting did not settle")
			}
			app.stopRunners()
			if calls.Load() != 1 || len(app.unfinishedRunners()) != 0 {
				t.Fatalf("calls=%d unfinished=%v", calls.Load(), app.unfinishedRunners())
			}
		})
	}
}

type installedWarmFixture struct {
	decide.Absent
	entered, release chan struct{}
	ready            atomic.Bool
}

func (*installedWarmFixture) Available() bool { return true }
func (w *installedWarmFixture) Warm(ctx context.Context) error {
	close(w.entered)
	select {
	case <-w.release:
		w.ready.Store(true)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestInstalledDecisionWarmCompletesBeforeStartupReturns(t *testing.T) {
	engine := &installedWarmFixture{entered: make(chan struct{}), release: make(chan struct{})}
	b := &serveBuilder{security: &security.Runtime{}}
	b.decisions.Decider = engine
	app := &ServeApp{}
	registerBackgroundRunners(b, app)
	// Structural recovery has its own fixture; retain only this registered preparation.
	for _, runner := range app.runners.runners {
		if runner.name == "decision-engine-warm" {
			app.runners.runners = []backgroundRunner{runner}
			break
		}
	}
	started := make(chan error, 1)
	go func() { started <- app.startRunners(t.Context()) }()
	select {
	case <-engine.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("installed engine warm never started")
	}
	select {
	case err := <-started:
		t.Fatalf("startup returned before installed warm completed: %v", err)
	default:
	}
	close(engine.release)
	select {
	case err := <-started:
		testutil.FailErr(t, "start installed engine", err)
	case <-time.After(5 * time.Second):
		t.Fatal("startup did not finish after warm")
	}
	t.Cleanup(app.stopRunners)
	if !engine.ready.Load() {
		t.Fatal("first request could observe unwarmed installed engine")
	}
}
