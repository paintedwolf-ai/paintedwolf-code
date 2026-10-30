//go:build unix

package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/osprocess"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type commandTurnResult struct {
	output string
	err    error
}

// A turn blocked in a command whose shell started a never-ending job in its
// own process group, the shape of a dev server under an interactive shell:
// abort must end the job, let the turn unwind, and settle the session.
func TestAbortEndsATurnBlockedInACommand(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	reg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	mgr.SetBackgroundRegistry(reg)
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark busy", st.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))

	programs := programtest.New(t, programtest.Options{Session: sess.ID, Background: reg})
	pidFile := filepath.Join(programs.Dir, "job.pid")
	testutil.FailErr(t, "write script", os.WriteFile(filepath.Join(programs.Dir, "serve.sh"),
		[]byte("set -m\nsleep 1000 &\necho $! > \"$1\"\nwait\n"), 0o600))

	turnDone := make(chan commandTurnResult, 1)
	go func() {
		lock := mgr.Runner.Execution.Prompt.Acquire(sess.ID)
		lock.Lock()
		defer lock.Unlock()
		turnCtx := mgr.Runner.Execution.Attach(context.Background(), sess.ID)
		defer mgr.Runner.Execution.Cancel(sess.ID)
		output, err := programs.Call(t, turnCtx, "sh serve.sh "+strconv.Quote(pidFile))
		turnDone <- commandTurnResult{output: output, err: err}
	}()
	job := waitForJobPID(t, pidFile)

	testutil.FailErr(t, "abort", mgr.Stops.Abort(ctx, sess.ID, "user stopped"))
	var turn commandTurnResult
	select {
	case turn = <-turnDone:
	case <-t.Context().Done():
		t.Fatal("the turn did not unwind after abort")
	}
	if !errors.Is(turn.err, context.Canceled) {
		t.Fatalf("turn result = %q, %v; want the cancelled command", turn.output, turn.err)
	}
	testutil.WaitFor(t, func() bool { return !osprocess.Alive(job) })
	got, err := st.Get(ctx, sess.ID)
	testutil.FailErr(t, "get session", err)
	if got.Status != api.SessionStatusIdle {
		t.Fatalf("status = %q, want idle", got.Status)
	}
	if reg.HasRunning(sess.ID) {
		t.Fatal("abort left a running command handle")
	}
}

func TestAbortStopsWithoutATurnThatNeverReleases(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.Stops.SetTurnReleaseTimeout(100 * time.Millisecond)
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark busy", st.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))

	stuck := mgr.Runner.Execution.Prompt.Acquire(sess.ID)
	stuck.Lock()
	testutil.FailErr(t, "abort", mgr.Stops.Abort(ctx, sess.ID, "user stopped"))
	got, err := st.Get(ctx, sess.ID)
	testutil.FailErr(t, "get session", err)
	if got.Status != api.SessionStatusIdle {
		t.Fatalf("status = %q, want idle", got.Status)
	}
	stuck.Unlock()
	// The abandoned acquisition releases the lock once the turn does.
	testutil.WaitFor(t, func() bool {
		probe := mgr.Runner.Execution.Prompt.Acquire(sess.ID)
		if !probe.TryLock() {
			return false
		}
		probe.Unlock()
		return true
	})
}

func waitForJobPID(t *testing.T, path string) int {
	t.Helper()
	var pid int
	testutil.WaitFor(t, func() bool {
		raw, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(raw)))
		return err == nil && pid > 0
	})
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}
