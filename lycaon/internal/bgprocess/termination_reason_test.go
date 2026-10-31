package bgprocess_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTerminationReasonEveryValueProducedByItsStopOrExitPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh/sleep fixtures are unix-oriented")
	}
	runner := hostcmd.NewRunner()

	expectCompletionAndExitEvent := func(t *testing.T, completed <-chan bgprocess.Completion, events <-chan api.BackgroundProcessEvent, want bgprocess.TerminationReason) {
		t.Helper()
		select {
		case c := <-completed:
			if c.TerminationReason != want {
				t.Fatalf("completion termination = %q, want %q", c.TerminationReason, want)
			}
		case <-t.Context().Done():
			t.Fatalf("timed out waiting for completion %q", want)
		}
		for {
			select {
			case ev := <-events:
				if ev.Stream == "exit" {
					if ev.TerminationReason != api.BackgroundProcessTerminationReason(want) {
						t.Fatalf("exit event termination = %q, want %q", ev.TerminationReason, want)
					}
					return
				}
			case <-t.Context().Done():
				t.Fatalf("timed out waiting for exit event %q", want)
			}
		}
	}

	t.Run("exited", func(t *testing.T) {
		completed := make(chan bgprocess.Completion, 2)
		events := make(chan api.BackgroundProcessEvent, 8)
		reg := newTestRegistry(t, bgprocess.DefaultConfig(), bgprocess.Hooks{
			Complete: func(_ context.Context, c bgprocess.Completion) { completed <- c },
			Publish:  func(_ context.Context, _, _ string, ev api.BackgroundProcessEvent) { events <- ev },
		})
		handle, err := startBackground(t.Context(), reg, "sess-1", "proj-1", hostcmd.Request{
			ProjectDir: t.TempDir(), Stages: []exec.Stage{{Name: "true"}},
		}, runner)
		testutil.FailErr(t, "start exited", err)
		_, err = reg.Lifecycle.Await(t.Context(), "sess-1", handle, 5*time.Second)
		testutil.FailErr(t, "await exited", err)
		expectCompletionAndExitEvent(t, completed, events, bgprocess.TerminationExited)
	})

	t.Run("timed_out", func(t *testing.T) {
		completed := make(chan bgprocess.Completion, 2)
		events := make(chan api.BackgroundProcessEvent, 8)
		reg := newTestRegistry(t, bgprocess.DefaultConfig(), bgprocess.Hooks{
			Complete: func(_ context.Context, c bgprocess.Completion) { completed <- c },
			Publish:  func(_ context.Context, _, _ string, ev api.BackgroundProcessEvent) { events <- ev },
		})
		handle, err := reg.StartPipeline(t.Context(), bgprocess.PipelineSpec{
			SessionID: "sess-1", ProjectID: "proj-1", Runner: runner, Mode: bgprocess.JobModeBackground,
			Timeout: 40 * time.Millisecond,
			Request: hostcmd.Request{
				Launch: exec.HostLaunch("timeout test"), ProjectDir: t.TempDir(),
				Stages: []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
			},
		})
		testutil.FailErr(t, "start timed_out", err)
		_, err = reg.Lifecycle.Await(t.Context(), "sess-1", handle, 5*time.Second)
		testutil.FailErr(t, "await timed_out", err)
		expectCompletionAndExitEvent(t, completed, events, bgprocess.TerminationTimedOut)
	})

	for _, reason := range []bgprocess.TerminationReason{
		bgprocess.TerminationStoppedByAgent,
		bgprocess.TerminationStoppedByUser,
	} {
		t.Run(string(reason), func(t *testing.T) {
			completed := make(chan bgprocess.Completion, 2)
			events := make(chan api.BackgroundProcessEvent, 8)
			reg := newTestRegistry(t, bgprocess.DefaultConfig(), bgprocess.Hooks{
				Complete: func(_ context.Context, c bgprocess.Completion) { completed <- c },
				Publish:  func(_ context.Context, _, _ string, ev api.BackgroundProcessEvent) { events <- ev },
			})
			handle, err := startBackground(t.Context(), reg, "sess-1", "proj-1", hostcmd.Request{
				ProjectDir: t.TempDir(), Stages: []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
			}, runner)
			testutil.FailErr(t, "start "+string(reason), err)
			res, err := reg.Lifecycle.Stop("sess-1", handle, reason)
			testutil.FailErr(t, "stop "+string(reason), err)
			if res.TerminationReason != api.BackgroundProcessTerminationReason(reason) {
				t.Fatalf("stop result reason = %q, want %q", res.TerminationReason, reason)
			}
			_, err = reg.Lifecycle.Await(t.Context(), "sess-1", handle, 5*time.Second)
			testutil.FailErr(t, "await "+string(reason), err)
			expectCompletionAndExitEvent(t, completed, events, reason)
		})
	}

	for _, reason := range []bgprocess.TerminationReason{
		bgprocess.TerminationChatDeleted,
		bgprocess.TerminationProjectDeleted,
		bgprocess.TerminationRootDetached,
		bgprocess.TerminationWorkerCompleted,
		bgprocess.TerminationWorkerCancelled,
	} {
		t.Run(string(reason), func(t *testing.T) {
			completed := make(chan bgprocess.Completion, 2)
			events := make(chan api.BackgroundProcessEvent, 8)
			reg := newTestRegistry(t, bgprocess.DefaultConfig(), bgprocess.Hooks{
				Complete: func(_ context.Context, c bgprocess.Completion) { completed <- c },
				Publish:  func(_ context.Context, _, _ string, ev api.BackgroundProcessEvent) { events <- ev },
			})
			_, err := startBackground(t.Context(), reg, "sess-1", "proj-1", hostcmd.Request{
				ProjectDir: t.TempDir(), Stages: []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
			}, runner)
			testutil.FailErr(t, "start "+string(reason), err)
			testutil.FailErr(t, "dispose "+string(reason), reg.Lifecycle.DisposeSession(t.Context(), "sess-1", reason))
			expectCompletionAndExitEvent(t, completed, events, reason)
		})
	}

	t.Run("app_quit", func(t *testing.T) {
		completed := make(chan bgprocess.Completion, 2)
		events := make(chan api.BackgroundProcessEvent, 8)
		reg := newTestRegistry(t, bgprocess.DefaultConfig(), bgprocess.Hooks{
			Complete: func(_ context.Context, c bgprocess.Completion) { completed <- c },
			Publish:  func(_ context.Context, _, _ string, ev api.BackgroundProcessEvent) { events <- ev },
		})
		_, err := startBackground(t.Context(), reg, "sess-1", "proj-1", hostcmd.Request{
			ProjectDir: t.TempDir(), Stages: []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
		}, runner)
		testutil.FailErr(t, "start app_quit", err)
		testutil.FailErr(t, "close registry", reg.Lifecycle.Close(t.Context()))
		expectCompletionAndExitEvent(t, completed, events, bgprocess.TerminationAppQuit)
	})
}

func TestStopOnSettledProcessPreservesExitedAndTimedOutReasons(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep/true fixtures are unix-oriented")
	}
	runner := hostcmd.NewRunner()

	for _, tc := range []struct {
		name    string
		timeout time.Duration
		stage   exec.Stage
		want    bgprocess.TerminationReason
	}{
		{name: "exited", stage: exec.Stage{Name: "true"}, want: bgprocess.TerminationExited},
		{name: "timed_out", timeout: 40 * time.Millisecond, stage: exec.Stage{Name: "sleep", Args: []string{"30"}}, want: bgprocess.TerminationTimedOut},
	} {
		t.Run(tc.name, func(t *testing.T) {
			completed := make(chan bgprocess.Completion, 4)
			events := make(chan api.BackgroundProcessEvent, 8)
			reg := newTestRegistry(t, bgprocess.DefaultConfig(), bgprocess.Hooks{
				Complete: func(_ context.Context, c bgprocess.Completion) { completed <- c },
				Publish:  func(_ context.Context, _, _ string, ev api.BackgroundProcessEvent) { events <- ev },
			})
			handle, err := reg.StartPipeline(t.Context(), bgprocess.PipelineSpec{
				SessionID: "sess-1", ProjectID: "proj-1", Runner: runner, Mode: bgprocess.JobModeBackground,
				Timeout: tc.timeout,
				Request: hostcmd.Request{
					Launch: exec.HostLaunch("settled stop test"), ProjectDir: t.TempDir(),
					Stages: []exec.Stage{tc.stage},
				},
			})
			testutil.FailErr(t, "start", err)
			_, err = reg.Lifecycle.Await(t.Context(), "sess-1", handle, 5*time.Second)
			testutil.FailErr(t, "await", err)
			<-completed

			stopRes, err := reg.Lifecycle.Stop("sess-1", handle, bgprocess.TerminationStoppedByUser)
			testutil.FailErr(t, "stop after settle", err)
			wantAPI := api.BackgroundProcessTerminationReason(tc.want)
			if stopRes.TerminationReason != wantAPI || stopRes.Running {
				t.Fatalf("stop result = %+v, want reason %q and running=false", stopRes, wantAPI)
			}
			out, err := reg.Output.ReadOutput(t.Context(), "sess-1", handle)
			testutil.FailErr(t, "read output", err)
			if out.TerminationReason != wantAPI {
				t.Fatalf("read output reason = %q, want %q", out.TerminationReason, wantAPI)
			}
			listed := reg.Output.List(t.Context(), "sess-1")
			if len(listed) != 1 || listed[0].TerminationReason != wantAPI {
				t.Fatalf("listed = %+v, want reason %q", listed, wantAPI)
			}
			select {
			case dup := <-completed:
				t.Fatalf("unexpected duplicate completion after Stop on settled process: %+v", dup)
			default:
			}
			for len(events) > 0 {
				ev := <-events
				if ev.Stream == "exit" && ev.TerminationReason != wantAPI {
					t.Fatalf("exit event overwritten to %q, want %q", ev.TerminationReason, wantAPI)
				}
			}
		})
	}
}

func TestConcurrentAndRepeatedStopsKeepFirstReasonAndEmitOneCompletion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	completed := make(chan bgprocess.Completion, 8)
	reg := newTestRegistry(t, bgprocess.DefaultConfig(), bgprocess.Hooks{
		Complete: func(_ context.Context, c bgprocess.Completion) { completed <- c },
	})
	handle, err := startBackground(t.Context(), reg, "sess-1", "proj-1", hostcmd.Request{
		ProjectDir: t.TempDir(), Stages: []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
	}, hostcmd.NewRunner())
	testutil.FailErr(t, "start", err)

	first, err := reg.Lifecycle.Stop("sess-1", handle, bgprocess.TerminationStoppedByAgent)
	testutil.FailErr(t, "first stop", err)
	if first.TerminationReason != api.BackgroundProcessTerminationReason(bgprocess.TerminationStoppedByAgent) {
		t.Fatalf("first stop reason = %q, want stopped_by_agent", first.TerminationReason)
	}

	var wg sync.WaitGroup
	for _, r := range []bgprocess.TerminationReason{
		bgprocess.TerminationStoppedByUser,
		bgprocess.TerminationChatDeleted,
		bgprocess.TerminationRootDetached,
	} {
		wg.Add(1)
		go func(reason bgprocess.TerminationReason) {
			defer wg.Done()
			res, stopErr := reg.Lifecycle.Stop("sess-1", handle, reason)
			if stopErr != nil {
				t.Errorf("concurrent stop(%s): %v", reason, stopErr)
				return
			}
			if res.TerminationReason != api.BackgroundProcessTerminationReason(bgprocess.TerminationStoppedByAgent) {
				t.Errorf("concurrent stop(%s) reason = %q, want stopped_by_agent", reason, res.TerminationReason)
			}
		}(r)
	}
	wg.Wait()

	_, err = reg.Lifecycle.Await(t.Context(), "sess-1", handle, 5*time.Second)
	testutil.FailErr(t, "await", err)

	select {
	case c := <-completed:
		if c.TerminationReason != bgprocess.TerminationStoppedByAgent {
			t.Fatalf("completion reason = %q, want stopped_by_agent", c.TerminationReason)
		}
	case <-t.Context().Done():
		t.Fatal("timed out waiting for completion")
	}
	select {
	case dup := <-completed:
		t.Fatalf("unexpected second completion: %+v", dup)
	default:
	}
}

func TestDisposeAwaitedSessionStopsOnlyUnpromotedAwaitedCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	runner := hostcmd.NewRunner()
	reg := newTestRegistry(t, bgprocess.DefaultConfig(), bgprocess.Hooks{})
	t.Cleanup(func() {
		_ = reg.Lifecycle.DisposeSession(context.Background(), "sess-1", bgprocess.TerminationChatDeleted)
	})

	bgHandle, err := reg.StartPipeline(t.Context(), bgprocess.PipelineSpec{
		SessionID: "sess-1", ProjectID: "proj-1", Runner: runner, Mode: bgprocess.JobModeBackground,
		Request: hostcmd.Request{
			Launch: exec.HostLaunch("bg survive test"), ProjectDir: t.TempDir(),
			Stages: []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
		},
	})
	testutil.FailErr(t, "start background", err)

	promotedHandle, err := reg.StartPipeline(t.Context(), bgprocess.PipelineSpec{
		SessionID: "sess-1", ProjectID: "proj-1", Runner: runner, Mode: bgprocess.JobModeAwaited,
		AllowConcurrent: true,
		Request: hostcmd.Request{
			Launch: exec.HostLaunch("promoted survive test"), ProjectDir: t.TempDir(),
			Stages: []exec.Stage{{Name: "sleep", Args: []string{"29"}}},
		},
	})
	testutil.FailErr(t, "start promoted", err)
	testutil.FailErr(t, "promote awaited command", reg.Output.Promote(t.Context(), "sess-1", promotedHandle))

	awaitedHandle, err := reg.StartPipeline(t.Context(), bgprocess.PipelineSpec{
		SessionID: "sess-1", ProjectID: "proj-1", Runner: runner, Mode: bgprocess.JobModeAwaited,
		AllowConcurrent: true,
		Request: hostcmd.Request{
			Launch: exec.HostLaunch("unpromoted awaited test"), ProjectDir: t.TempDir(),
			Stages: []exec.Stage{{Name: "sleep", Args: []string{"28"}}},
		},
	})
	testutil.FailErr(t, "start unpromoted awaited", err)

	testutil.FailErr(t, "dispose awaited session", reg.Lifecycle.DisposeAwaitedSession(t.Context(), "sess-1", bgprocess.TerminationStoppedByUser))

	if _, err := reg.Snapshot(t.Context(), "sess-1", awaitedHandle, 0); err == nil {
		t.Fatal("expected unpromoted awaited handle to be discarded on DisposeAwaitedSession")
	}
	for _, keep := range []string{bgHandle, promotedHandle} {
		snap, err := reg.Snapshot(t.Context(), "sess-1", keep, 0)
		testutil.FailErr(t, "snapshot surviving handle "+keep, err)
		if snap.HasExit {
			t.Fatalf("handle %s stopped during DisposeAwaitedSession", keep)
		}
	}
	active := reg.ActiveJobs("sess-1")
	if len(active) != 2 {
		t.Fatalf("active jobs = %+v, want 2 surviving jobs", active)
	}
}

func TestProcessesUnderRootAndStopUnderRootCanonicalContainment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep/symlink fixture is unix-oriented")
	}
	runner := hostcmd.NewRunner()
	completed := make(chan bgprocess.Completion, 8)
	reg := newTestRegistry(t, bgprocess.DefaultConfig(), bgprocess.Hooks{
		Complete: func(_ context.Context, c bgprocess.Completion) { completed <- c },
	})
	t.Cleanup(func() {
		_ = reg.Lifecycle.Close(context.Background())
	})

	rootA := t.TempDir()
	nestedA := filepath.Join(rootA, "sub", "nested")
	testutil.FailErr(t, "mkdir nestedA", os.MkdirAll(nestedA, 0o755))
	rootB := t.TempDir()
	symlinkHolder := t.TempDir()
	symlinkToA := filepath.Join(symlinkHolder, "link-to-a")
	testutil.FailErr(t, "symlink to rootA", os.Symlink(filepath.Join(rootA, "sub"), symlinkToA))

	hRootA, err := startBackground(t.Context(), reg, "sess-1", "proj-1", hostcmd.Request{
		ProjectDir: rootA, Stages: []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
	}, runner)
	testutil.FailErr(t, "start rootA", err)

	hNestedA, err := startBackground(t.Context(), reg, "sess-1", "proj-1", hostcmd.Request{
		ProjectDir: nestedA, Stages: []exec.Stage{{Name: "sleep", Args: []string{"29"}}},
	}, runner)
	testutil.FailErr(t, "start nestedA", err)

	hSymlinkA, err := startBackground(t.Context(), reg, "sess-2", "proj-1", hostcmd.Request{
		ProjectDir: symlinkToA, Stages: []exec.Stage{{Name: "sleep", Args: []string{"28"}}},
	}, runner)
	testutil.FailErr(t, "start symlinkA", err)

	hRootB, err := startBackground(t.Context(), reg, "sess-1", "proj-1", hostcmd.Request{
		ProjectDir: rootB, Stages: []exec.Stage{{Name: "sleep", Args: []string{"27"}}},
	}, runner)
	testutil.FailErr(t, "start rootB", err)

	underA := reg.ProcessesUnderRoot("proj-1", nil, rootA)
	if len(underA) != 3 {
		t.Fatalf("ProcessesUnderRoot(rootA) = %+v, want 3 processes", underA)
	}
	for _, p := range underA {
		if p.Handle == hRootB {
			t.Fatalf("ProcessesUnderRoot(rootA) included rootB handle %s", hRootB)
		}
	}

	testutil.FailErr(t, "StopUnderRoot(rootA)", reg.Lifecycle.StopUnderRoot(t.Context(), "proj-1", nil, rootA, underA, bgprocess.TerminationRootDetached))

	for _, stoppedHandle := range []struct {
		sess   string
		handle string
	}{
		{"sess-1", hRootA},
		{"sess-1", hNestedA},
		{"sess-2", hSymlinkA},
	} {
		snap, err := reg.Snapshot(t.Context(), stoppedHandle.sess, stoppedHandle.handle, 0)
		testutil.FailErr(t, "snapshot stopped handle", err)
		if !snap.HasExit || snap.TerminationReason != bgprocess.TerminationRootDetached {
			t.Fatalf("handle %s state = (hasExit=%v, reason=%q), want (true, root_detached)", stoppedHandle.handle, snap.HasExit, snap.TerminationReason)
		}
	}

	snapB, err := reg.Snapshot(t.Context(), "sess-1", hRootB, 0)
	testutil.FailErr(t, "snapshot rootB handle", err)
	if snapB.HasExit {
		t.Fatal("expected process under rootB to keep running after detaching rootA")
	}
}
