package app

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWatchParentExitUnarmedWithoutEnv(t *testing.T) {
	t.Setenv(ParentPIDEnv, "")
	if ch := watchParentExit(t.Context()); ch != nil {
		t.Fatal("watchdog armed with no parent pid set")
	}
}

func TestWatchParentExitIgnoresUnusablePID(t *testing.T) {
	for _, raw := range []string{"not-a-number", "0", "1", "-5"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv(ParentPIDEnv, raw)
			// PID 1 is the reparent target, so its watchdog cannot fire.
			if ch := watchParentExit(t.Context()); ch != nil {
				t.Fatalf("watchdog armed for unusable pid %q", raw)
			}
		})
	}
}

func TestWatchParentExitStaysQuietWhileParentLives(t *testing.T) {
	testutil.SkipIfShort(t, "waits through two parent-process polling intervals")
	t.Setenv(ParentPIDEnv, strconv.Itoa(os.Getpid()))
	gone := watchParentExit(t.Context())
	if gone == nil {
		t.Fatal("watchdog not armed for a live parent")
	}
	select {
	case <-gone:
		t.Fatal("watchdog fired while its parent was still running")
	case <-time.After(parentPollInterval * 2):
	}
}

func TestWatchParentExitFiresWhenParentDies(t *testing.T) {
	testutil.SkipIfShort(t, "spawns and polls a stand-in parent process")
	if runtime.GOOS == "windows" {
		t.Skip("no signal-0 existence check on windows; watchdog is inert there")
	}
	// A real process exercises the OS liveness check.
	proc := exec.Command("sleep", "30")
	testutil.FailErr(t, "start stand-in parent", proc.Start())
	t.Setenv(ParentPIDEnv, strconv.Itoa(proc.Process.Pid))

	gone := watchParentExit(t.Context())
	if gone == nil {
		t.Fatal("watchdog not armed")
	}

	// SIGKILL models a parent that exits without cleanup.
	testutil.FailErr(t, "kill stand-in parent", proc.Process.Kill())
	_ = proc.Wait()

	select {
	case <-gone:
	case <-time.After(parentPollInterval * 5):
		t.Fatal("watchdog never noticed its parent had gone")
	}
}

func TestWatchParentExitStopsWithContext(t *testing.T) {
	t.Setenv(ParentPIDEnv, strconv.Itoa(os.Getpid()))
	ctx, cancel := context.WithCancel(t.Context())
	gone := watchParentExit(ctx)
	if gone == nil {
		t.Fatal("watchdog not armed")
	}

	cancel()
	select {
	case <-gone:
	case <-time.After(parentPollInterval * 3):
		t.Fatal("watchdog goroutine outlived its context")
	}
}
