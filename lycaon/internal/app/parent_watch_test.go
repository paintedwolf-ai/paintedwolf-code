package app

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"github.com/lycaon/lycaon/internal/app/persistence"
	"github.com/lycaon/lycaon/internal/app/sessions"
	"github.com/lycaon/lycaon/internal/app/workerruntime"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/coordinatorcontrol"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
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
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		gone := watchParentExit(ctx)
		if gone == nil {
			t.Fatal("watchdog not armed for a live parent")
		}
		time.Sleep(parentPollInterval * 2)
		synctest.Wait()
		select {
		case <-gone:
			t.Fatal("watchdog fired while its parent was still running")
		default:
		}
	})
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
	case <-t.Context().Done():
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
	case <-t.Context().Done():
		t.Fatal("watchdog goroutine outlived its context")
	}
}

func TestQuietRoundsWiring(t *testing.T) {
	ctx := t.Context()
	store := testdbfixture.Open(t, "quiet-rounds.db")
	cfg := worker.DefaultWorkersConfig()
	b := &serveBuilder{
		storage:       persistence.Runtime{Database: store},
		workerRuntime: workerruntime.New(store, nil, cfg),
		sessions: &sessions.Runtime{
			Manager:     &session.Host{Coordinator: &coordinatorcontrol.Service{Workers: &coordinatorcontrol.Workers{}}},
			Checkpoints: &hitl.Checkpoints{Store: hitl.NewSQLStore(store)},
		},
		worker: workerCoordination{cfg: cfg},
	}
	quiet := b.quietRounds()
	jobs, err := quiet.Jobs(ctx)
	testutil.FailErr(t, "quiet.Jobs", err)
	if len(jobs) != 0 {
		t.Fatalf("len(jobs) = %d, want 0", len(jobs))
	}
	ts, err := quiet.Progress(ctx, "child-1")
	testutil.FailErr(t, "quiet.Progress empty", err)
	if !ts.IsZero() {
		t.Fatalf("empty progress = %v, want zero", ts)
	}
	now := "2026-01-02T03:04:05Z"
	testdbseed.InsertProject(t, store, "p1")
	testdbseed.InsertSession(t, store, "child-1", "p1")
	_, err = store.ExecContext(ctx, `INSERT INTO turns(id,session_id,project_id,origin,input_json,status,created_at,updated_at,progressed_at) VALUES('t1','child-1','p1','worker','','running',?,?,?)`, now, now, now)
	testutil.FailErr(t, "insert turn", err)
	ts, err = quiet.Progress(ctx, "child-1")
	testutil.FailErr(t, "quiet.Progress populated", err)
	if ts.UTC().Format(time.RFC3339) != now {
		t.Fatalf("progress = %s, want %s", ts.UTC().Format(time.RFC3339), now)
	}
	if quiet.AwaitingApproval == nil || quiet.AwaitingApproval(ctx, "child-1") {
		t.Fatalf("AwaitingApproval = true or nil, want false")
	}
}
