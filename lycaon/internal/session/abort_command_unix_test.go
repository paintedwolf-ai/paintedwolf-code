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
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/osprocess"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
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
	mgr := NewManager(st, nil, nil, settings.DefaultSessionLimits())
	reg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	mgr.SetBackgroundRegistry(reg)
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark busy", st.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))

	root := t.TempDir()
	pidFile := filepath.Join(root, "job.pid")
	testutil.FailErr(t, "write script", os.WriteFile(filepath.Join(root, "serve.sh"),
		[]byte("set -m\nsleep 1000 &\necho $! > \"$1\"\nwait\n"), 0o600))
	tool := &native.CommandTool{
		Runner: hostcmd.NewRunner(),
		Boundary: sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{
			{ID: "implement", Tools: map[string]bool{"command": true}},
		}),
		Background: reg,
	}
	tctx := tools.ToolContext{
		SessionID: sess.ID, Agent: "implement",
		Roots: []projectroot.RootRef{{ID: "primary", Path: root, IsPrimary: true}},
	}

	turnDone := make(chan commandTurnResult, 1)
	go func() {
		lock := mgr.promptState.Prompt.Acquire(sess.ID)
		lock.Lock()
		defer lock.Unlock()
		turnCtx := mgr.attachPromptCancel(context.Background(), sess.ID)
		defer mgr.detachPromptCancel(sess.ID)
		output, err := tool.Run(turnCtx, map[string]any{"command": "sh serve.sh " + pidFile}, tctx)
		turnDone <- commandTurnResult{output: output, err: err}
	}()
	job := waitForJobPID(t, pidFile)

	started := time.Now()
	testutil.FailErr(t, "abort", mgr.Abort(ctx, sess.ID, "user stopped"))
	if elapsed := time.Since(started); elapsed > exec.TerminateGrace+10*time.Second {
		t.Fatalf("abort took %v", elapsed)
	}
	var turn commandTurnResult
	select {
	case turn = <-turnDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn did not unwind after abort")
	}
	if !errors.Is(turn.err, context.Canceled) {
		t.Fatalf("turn result = %q, %v; want the cancelled command", turn.output, turn.err)
	}
	testutil.WaitFor(t, 5*time.Second, func() bool { return !osprocess.Alive(job) })
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
	mgr := NewManager(st, nil, nil, settings.DefaultSessionLimits())
	mgr.turnReleaseTimeout = 100 * time.Millisecond
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark busy", st.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))

	stuck := mgr.promptState.Prompt.Acquire(sess.ID)
	stuck.Lock()
	started := time.Now()
	testutil.FailErr(t, "abort", mgr.Abort(ctx, sess.ID, "user stopped"))
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("abort waited %v on a turn that never released", elapsed)
	}
	got, err := st.Get(ctx, sess.ID)
	testutil.FailErr(t, "get session", err)
	if got.Status != api.SessionStatusIdle {
		t.Fatalf("status = %q, want idle", got.Status)
	}
	stuck.Unlock()
	// The abandoned acquisition releases the lock once the turn does.
	testutil.WaitFor(t, 5*time.Second, func() bool {
		probe := mgr.promptState.Prompt.Acquire(sess.ID)
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
	testutil.WaitFor(t, 20*time.Second, func() bool {
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
