package bgprocess

import (
	"context"
	"errors"
	"io"
	"runtime"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandJobAdmissionPrecedesPreparation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix command fixtures")
	}
	for _, door := range []struct {
		name string
		mode JobMode
		want error
		call func(context.Context, *Registry, hostcmd.Request, *hostcmd.Runner) error
	}{
		{"pipeline", JobModeAwaited, ErrAwaitedCapReached, func(ctx context.Context, r *Registry, req hostcmd.Request, runner *hostcmd.Runner) error {
			_, err := r.StartPipeline(ctx, PipelineSpec{SessionID: "session", Request: req, Runner: runner, Mode: JobModeAwaited, AllowConcurrent: true})
			return err
		}},
		{"pty capture", JobModeAwaited, ErrAwaitedCapReached, func(ctx context.Context, r *Registry, req hostcmd.Request, runner *hostcmd.Runner) error {
			_, err := r.Terminal.RunPTYCapture(ctx, "session", "", "", req, runner, exec.WinSize{}, confine.SpawnFacts{}, time.Second)
			return err
		}},
		{"pty open", JobModeBackground, ErrBackgroundCapReached, func(ctx context.Context, r *Registry, req hostcmd.Request, runner *hostcmd.Runner) error {
			_, err := r.Terminal.StartPTY(ctx, "session", "", "", req, runner, exec.WinSize{}, confine.SpawnFacts{})
			return err
		}},
	} {
		t.Run(door.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.MaxAwaited, cfg.MaxBackground = 1, 1
			r := newTestRegistry(t, cfg, Hooks{})
			dir, runner := t.TempDir(), hostcmd.NewRunner()
			input, writer := io.Pipe()
			defer func() { _ = writer.Close() }()
			req := hostcmd.Request{ProjectDir: dir, Stages: []exec.Stage{{Name: "/bin/cat"}}, Launch: exec.HostLaunch("fixture")}
			req.Stdin = &exec.StdinSpec{Reader: input}
			_, err := r.StartPipeline(t.Context(), PipelineSpec{SessionID: "session", Request: req, Runner: runner, Mode: door.mode})
			testutil.FailErr(t, "occupy capacity", err)
			req.Stdin = nil
			req.Stages = []exec.Stage{{Name: "/bin/true"}}
			var preparations int
			req.BeforeStart = func(context.Context) (hostcmd.SourceIdentity, error) {
				preparations++
				return hostcmd.SourceIdentity{}, nil
			}
			if err := door.call(t.Context(), r, req, runner); !errors.Is(err, door.want) || preparations != 0 {
				t.Fatalf("preparations=%d, err=%v, want %v before preparation", preparations, err, door.want)
			}
		})
	}
}

func TestPreparationUsesJobLifetime(t *testing.T) {
	runCtx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	req := hostcmd.Request{BeforeStart: func(context.Context) (hostcmd.SourceIdentity, error) {
		called = true
		return hostcmd.SourceIdentity{}, nil
	}}
	_, err := prepareJobStart(t.Context(), runCtx, req)
	if called || !errors.Is(err, context.Canceled) {
		t.Fatalf("preparation called=%v, err=%v", called, err)
	}
}
