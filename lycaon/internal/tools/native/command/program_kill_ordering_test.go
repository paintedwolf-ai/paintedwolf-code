package command_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A lost signal lets the job finish on its own and report 0, so the status decides
// this and no deadline does. The held threshold keeps load from parking the wait.
func TestProgramKillRightAfterBackgroundReportsItsSignal(t *testing.T) {
	const concurrentPrograms = 16
	for name, job := range map[string]string{
		"native":   "sleep 30",
		"external": "external -- /bin/sleep 30",
	} {
		t.Run(name, func(t *testing.T) {
			program := job + ` & kill %1; wait %1; printf 'job:%s\n' "$?"; ` +
				job + ` & p=$!; kill "$p"; wait "$p"; printf 'pid:%s\n' "$?"; ` +
				job + ` & kill %%; wait; printf 'bare:%s\n' "$?"`
			fixtures := make([]*windowFixture, concurrentPrograms)
			for i := range fixtures {
				fixtures[i] = newWindowFixture(t)
				fixtures[i].Runtime.ParkThreshold = time.Hour
			}
			outcomes := make([]string, concurrentPrograms)
			var wg sync.WaitGroup
			for i, f := range fixtures {
				wg.Add(1)
				go func() {
					defer wg.Done()
					content, err := f.Call(t, t.Context(), program)
					if err != nil {
						t.Errorf("program %d: %v", i, err)
						return
					}
					outcomes[i] = decodeOutcome(t, content).Stdout
				}()
			}
			wg.Wait()
			for i, stdout := range outcomes {
				if stdout != "job:143\npid:143\nbare:0\n" {
					t.Errorf("program %d printed %q", i, stdout)
				}
			}
		})
	}
}

// The launch is held before its process exists, so the job is listed with nothing to
// signal yet. Its stop still has to decide the status, and no process may start after.
func TestProgramKillBeforeTheProcessStartsReportsItsSignal(t *testing.T) {
	f := newWindowFixture(t)
	f.Runtime.ParkThreshold = time.Hour
	release := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	t.Cleanup(open)
	held := make(chan struct{})
	var heldOnce sync.Once
	f.opener.before = func(ctx context.Context) error {
		heldOnce.Do(func() { close(held) })
		select {
		case <-release:
		case <-ctx.Done():
		}
		return ctx.Err()
	}
	done := make(chan callResult, 1)
	go func() {
		content, err := f.Call(t, t.Context(), `external -- /bin/sh -c 'echo ran > started.txt' & wait %1; printf 'job:%s\n' "$?"; test -e started.txt; printf 'started:%s\n' "$?"`)
		done <- callResult{content, err}
	}()
	testutil.AwaitClosed(t, "the launch reaches its hold", held)
	content, err := f.Call(t, t.Context(), `kill %1`)
	testutil.FailErr(t, "kill the held job", err)
	if got := decodeOutcome(t, content); got.Status != 0 {
		t.Fatalf("kill outcome: %+v", got)
	}
	jobs := f.Runtime.Supervisor.Jobs(f.Session, testdbseed.DefaultProjectID)
	testutil.WaitFor(t, func() bool {
		live, err := jobs.List(t.Context())
		return err == nil && len(live) == 0
	})
	// The rest of the program may launch through the same window.
	open()
	result := <-done
	testutil.FailErr(t, "run the held program", result.err)
	if got := decodeOutcome(t, result.content); got.Stdout != "job:143\nstarted:1\n" {
		t.Fatalf("held job outcome: %+v", got)
	}
}
