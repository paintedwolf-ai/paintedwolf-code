package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/observability"
)

// serveRunnerDrainTimeout bounds runner shutdown before lock release. It fits
// inside the ordered shutdown budget so a runner that ignores cancellation
// cannot push the engine past the shell's graceful-stop wait.
var serveRunnerDrainTimeout = serveDrainTimeout

type backgroundRunner struct {
	name       string
	run        func(context.Context) error
	oneShot    bool
	retryDelay func(attempt int) time.Duration
	// done identifies runners still active at the drain deadline.
	done chan struct{}
}

const (
	backgroundRunnerRetryMin  = 250 * time.Millisecond
	backgroundRunnerRetryMax  = 30 * time.Second
	backgroundRunnerStableRun = time.Minute
)

var serveRunnerOrder = []string{
	"boot-recovery",
	"store-coupled-reconcile",
	"wal-checkpointer",
	"store-maintenance",
	"store-integrity-audit",
	"managed-secret-maintenance",
	"vault-unlock-sweep",
	"worker-poller",
	"scan-runner",
	"scan-cadence",
	"warm-runner",
	"decision-engine-warm",
	"source-blob-gc",
	"content-blob-gc",
	"prompt-attachment-maintenance",
	"content-density",
	"history-retention",
	"debug-retention",
	"worker-branch-retention",
}

func (a *ServeApp) registerRunner(name string, run func(context.Context) error) {
	a.runners = append(a.runners, backgroundRunner{name: name, run: run, retryDelay: backgroundRunnerBackoff})
}

func (a *ServeApp) registerOneShotRunner(name string, run func(context.Context) error) {
	a.runners = append(a.runners, backgroundRunner{name: name, run: run, oneShot: true})
}

func (a *ServeApp) startRunners(ctx context.Context) error {
	if a.runnersActive || len(a.runners) == 0 {
		return nil
	}
	names := make(map[string]struct{}, len(a.runners))
	for i := range a.runners {
		name := strings.TrimSpace(a.runners[i].name)
		if name == "" || a.runners[i].run == nil {
			return fmt.Errorf("background runner %d requires a name and function", i)
		}
		if !a.runners[i].oneShot && a.runners[i].retryDelay == nil {
			return fmt.Errorf("background runner %q requires a retry policy", name)
		}
		if _, duplicate := names[name]; duplicate {
			return fmt.Errorf("background runner %q registered twice", name)
		}
		names[name] = struct{}{}
	}
	runnerCtx, cancel := context.WithCancel(ctx)
	a.runnersCancel = cancel
	a.runnersActive = true
	for i := range a.runners {
		r := &a.runners[i]
		if !r.oneShot {
			continue
		}
		r.done = make(chan struct{})
		err := runBackgroundRunnerAttempt(runnerCtx, r)
		close(r.done)
		if err == nil {
			continue
		}
		stopErr := ctx.Err()
		cancel()
		a.runnersCancel = nil
		a.runnersActive = false
		if stopErr != nil {
			return stopErr
		}
		return fmt.Errorf("background startup runner %s: %w", r.name, err)
	}
	for i := range a.runners {
		if a.runners[i].oneShot {
			continue
		}
		a.runners[i].done = make(chan struct{})
		r := &a.runners[i]
		a.runnersWG.Add(1)
		go func() {
			// Accounting remains outside panic recovery.
			defer a.runnersWG.Done()
			defer close(r.done)
			a.superviseRunner(runnerCtx, r)
		}()
	}
	return nil
}

func (a *ServeApp) superviseRunner(ctx context.Context, runner *backgroundRunner) {
	attempt := 0
	for {
		started := time.Now()
		err := runBackgroundRunnerAttempt(ctx, runner)
		if ctx.Err() != nil || a.storeFailure() != nil {
			return
		}
		if time.Since(started) >= backgroundRunnerStableRun {
			attempt = 0
		}
		attempt++
		delay := runner.retryDelay(attempt)
		slog.ErrorContext(ctx, "background runner stopped; restarting",
			"runner", runner.name, "error", err, "retry_in", delay, "attempt", attempt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func runBackgroundRunnerAttempt(ctx context.Context, runner *backgroundRunner) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			observability.LogRecoveredPanic("app.runner."+runner.name, recovered)
			err = fmt.Errorf("runner panic: %v", recovered)
		}
	}()
	err = runner.run(ctx)
	if err == nil && ctx.Err() == nil && !runner.oneShot {
		return errors.New("runner exited without cancellation")
	}
	return err
}

func backgroundRunnerBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := backgroundRunnerRetryMin
	for i := 1; i < attempt && delay < backgroundRunnerRetryMax; i++ {
		delay *= 2
	}
	if delay > backgroundRunnerRetryMax {
		return backgroundRunnerRetryMax
	}
	return delay
}

func (a *ServeApp) stopRunners() {
	ctx, cancel := context.WithTimeout(context.Background(), serveRunnerDrainTimeout)
	defer cancel()
	a.stopRunnersWithin(ctx)
}

// stopRunnersWithin cancels the runners and waits for them, giving up at the
// earlier of the caller's deadline and the runner drain timeout.
func (a *ServeApp) stopRunnersWithin(ctx context.Context) {
	if !a.runnersActive {
		return
	}
	if a.runnersCancel != nil {
		a.runnersCancel()
		a.runnersCancel = nil
	}
	drained := make(chan struct{})
	go func() {
		a.runnersWG.Wait()
		close(drained)
	}()
	timer := time.NewTimer(serveRunnerDrainTimeout)
	defer timer.Stop()
	select {
	case <-drained:
	case <-ctx.Done():
		slog.WarnContext(ctx, "background runners did not stop before the shutdown deadline; continuing shutdown",
			"runners", a.unfinishedRunners())
	case <-timer.C:
		slog.WarnContext(ctx, "background runners did not stop; continuing shutdown",
			"runners", a.unfinishedRunners(), "timeout", serveRunnerDrainTimeout)
	}
	a.runnersActive = false
}

// unfinishedRunners names the runners still executing, for the drain warning.
func (a *ServeApp) unfinishedRunners() []string {
	var names []string
	for i := range a.runners {
		if a.runners[i].done == nil {
			continue
		}
		select {
		case <-a.runners[i].done:
		default:
			names = append(names, a.runners[i].name)
		}
	}
	return names
}
