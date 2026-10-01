package bgprocess

import (
	"context"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	lycexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
)

// PTYCaptureResult holds output from one sealed terminal run.
type PTYCaptureResult struct {
	Screen   ScreenSnapshot
	ExitCode int
	TimedOut bool
	Boundary confine.Boundary
}

// RunPTYCapture executes one command and discards its terminal handle.
func (r *Registry) RunPTYCapture(
	ctx context.Context,
	sessionID, rootSessionID, projectID string,
	req hostcmd.Request,
	runner *hostcmd.Runner,
	winsize lycexec.WinSize,
	facts confine.SpawnFacts,
	timeout time.Duration,
) (PTYCaptureResult, error) {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	handle, err := r.startPTY(ctx, sessionID, rootSessionID, projectID, req, runner, winsize, facts, true)
	if err != nil {
		return PTYCaptureResult{}, err
	}
	defer r.Discard(sessionID, handle)
	finished, err := r.Await(ctx, sessionID, handle, timeout)
	if err != nil {
		if proc, lookupErr := r.lookup(sessionID, handle); lookupErr == nil {
			r.killProcess(proc)
		}
		return PTYCaptureResult{}, err
	}
	var timedOutScreen *ScreenSnapshot
	if !finished {
		proc, lookupErr := r.lookup(sessionID, handle)
		if lookupErr != nil {
			return PTYCaptureResult{}, lookupErr
		}
		preStop, snapshotErr := r.SnapshotPTY(ctx, sessionID, handle, PTYReadOpts{
			Idle: DefaultPTYIdle, Timeout: DefaultPTYReadTimeout,
		})
		if snapshotErr != nil {
			return PTYCaptureResult{}, snapshotErr
		}
		timedOutScreen = &preStop.Screen
		r.killProcess(proc)
		select {
		case <-proc.done:
		case <-time.After(2 * time.Second):
			return PTYCaptureResult{}, fmt.Errorf("sealed pty did not stop after timeout")
		}
	}
	snapshot, err := r.SnapshotPTY(ctx, sessionID, handle, PTYReadOpts{
		Idle: DefaultPTYIdle, Timeout: DefaultPTYReadTimeout,
	})
	if err != nil {
		return PTYCaptureResult{}, err
	}
	if timedOutScreen != nil {
		snapshot.Screen = *timedOutScreen
	}
	exitCode := -1
	if snapshot.ExitCode != nil {
		exitCode = *snapshot.ExitCode
	}
	return PTYCaptureResult{
		Screen: snapshot.Screen, ExitCode: exitCode, TimedOut: !finished,
		Boundary: confine.BoundaryOf(req.Launch.Confinement),
	}, nil
}
