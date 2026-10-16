package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/app/security"
	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/testutil"
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
	app.runners.runners[0].retryDelay = func(int) time.Duration { return time.Millisecond }
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
	app.runners.runners[0].retryDelay = func(int) time.Duration { return time.Millisecond }
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
	var attempts, backgroundAttempts atomic.Int32
	app.registerBackgroundOneShotRunner("optional", func(context.Context) error { backgroundAttempts.Add(1); return nil })
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
	if app.runners.active || backgroundAttempts.Load() != 0 {
		t.Fatal("runners remained active or optional work ran after startup failure")
	}
}

func TestReportRecoveryBlocksNormalServing(t *testing.T) {
	boom := errors.New("source mutation journal remained unsettled")
	b := &serveBuilder{}
	err := b.reportRecovery(bootrecovery.Report{
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
		"invalid mode": func(app *ServeApp) {
			app.registerBackgroundOneShotRunner("warm", func(context.Context) error { return nil })
			app.runners.runners[0].mode = runnerMode(255)
		},
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
			if app.runners.active {
				t.Fatal("malformed registry started runners")
			}
		})
	}
}

func TestDecisionBootstrapRunnerDoesNotRequireInstalledCheckpoint(t *testing.T) {
	binary, err := os.Executable()
	testutil.FailErr(t, "locate fixture executable", err)
	for _, tc := range []struct {
		name                               string
		disabled, missingBinary, installed bool
		want                               bool
	}{
		{name: "missing checkpoint", want: true},
		{name: "installed checkpoint", installed: true, want: true},
		{name: "disabled", disabled: true},
		{name: "missing executable", missingBinary: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "checkpoint")
			executable := binary
			if tc.missingBinary {
				executable = filepath.Join(t.TempDir(), "absent")
			}
			if tc.installed {
				for _, file := range append(bialy.ShippedModel.Files, bialy.ModelFile{Path: bialy.CompleteMarker}) {
					path := filepath.Join(dir, filepath.FromSlash(file.Path))
					testutil.FailErr(t, "create checkpoint fixture directory", os.MkdirAll(filepath.Dir(path), 0o700))
					testutil.FailErr(t, "create installed checkpoint fixture", os.WriteFile(path, nil, 0o600))
				}
			}
			client := bialy.New(bialy.Config{Binary: executable, ModelDir: dir, Disabled: tc.disabled})
			b := &serveBuilder{security: &security.Runtime{}}
			b.decisions.Decider = client
			app := &ServeApp{}
			registerBackgroundRunners(b, app)
			found := false
			for _, runner := range app.runners.runners {
				if runner.name != "decision-engine-warm" {
					continue
				}
				found = true
				wantMode := runnerBackgroundOnce
				if tc.installed {
					wantMode = runnerStartupOnce
				}
				if runner.mode != wantMode {
					t.Fatalf("bootstrap mode=%v want=%v", runner.mode, wantMode)
				}
				if tc.installed {
					continue
				}
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				testutil.FailErr(t, "run canceled bootstrap", runner.run(ctx))
			}
			if found != tc.want {
				t.Fatalf("registered=%v want=%v", found, tc.want)
			}
			_, err := os.Stat(dir)
			if tc.want && err != nil {
				t.Fatalf("registered bootstrap never reached provisioning: %v", err)
			}
			if !tc.want && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("ineligible bootstrap changed model destination: %v", err)
			}
			if client.Available() != tc.installed {
				t.Fatal("bootstrap changed checkpoint readiness")
			}
		})
	}
}
