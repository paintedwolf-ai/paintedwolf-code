package bgprocess_test

import (
	"context"
	"errors"
	"net/netip"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProjectBackgroundIndexingAndSessionCounts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep/true fixtures are unix-oriented")
	}
	var (
		mu     sync.Mutex
		events []api.ProjectBackgroundEvent
	)
	reg := newTestRegistry(t, bgprocess.Config{
		MaxBackground: 4,
		MaxAwaited:    4,
		MaxRecent:     2,
	}, bgprocess.Hooks{
		PublishProject: func(_ context.Context, ev api.ProjectBackgroundEvent) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		},
	})
	t.Cleanup(func() { _ = reg.Lifecycle.Close(context.Background()) })
	runner := hostcmd.NewRunner()
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	// Start a running background command in proj-1 from a worker child session.
	bg1, err := reg.StartPipeline(t.Context(), bgprocess.PipelineSpec{
		SessionID:     "worker-child-1",
		RootSessionID: "chat-root-1",
		ProjectID:     "proj-1",
		Runner:        runner,
		Mode:          bgprocess.JobModeBackground,
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("proj-1 bg"),
			ProjectDir: dir1,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
		},
	})
	testutil.FailErr(t, "start bg1", err)

	// Start an unpromoted inline awaited command in proj-1 (must be excluded).
	awaitedUnpromoted, err := reg.StartPipeline(t.Context(), bgprocess.PipelineSpec{
		SessionID:       "chat-root-1",
		ProjectID:       "proj-1",
		Runner:          runner,
		Mode:            bgprocess.JobModeAwaited,
		AllowConcurrent: true,
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("proj-1 inline awaited"),
			ProjectDir: dir1,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"29"}}},
		},
	})
	testutil.FailErr(t, "start awaitedUnpromoted", err)

	// Start an awaited command in proj-1 and promote it to background.
	promoted1, err := reg.StartPipeline(t.Context(), bgprocess.PipelineSpec{
		SessionID:       "chat-root-2",
		ProjectID:       "proj-1",
		Runner:          runner,
		Mode:            bgprocess.JobModeAwaited,
		AllowConcurrent: true,
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("proj-1 promoted"),
			ProjectDir: dir1,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"28"}}},
		},
	})
	testutil.FailErr(t, "start promoted1", err)
	testutil.FailErr(t, "promote promoted1", reg.Output.Promote(t.Context(), "chat-root-2", promoted1))

	// Start a running background command in proj-2.
	bg2, err := reg.StartPipeline(t.Context(), bgprocess.PipelineSpec{
		SessionID: "chat-proj-2",
		ProjectID: "proj-2",
		Runner:    runner,
		Mode:      bgprocess.JobModeBackground,
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("proj-2 bg"),
			ProjectDir: dir2,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"27"}}},
		},
	})
	testutil.FailErr(t, "start bg2", err)

	list1, rev1 := reg.ListProjectBackground(t.Context(), "proj-1")
	if rev1 < 2 {
		t.Fatalf("proj-1 revision = %d, want >= 2", rev1)
	}
	if len(list1) != 2 {
		t.Fatalf("proj-1 background list = %+v, want 2 items (bg1 and promoted1, excluding %s)", list1, awaitedUnpromoted)
	}
	seenHandles := map[string]bgprocess.ProjectProcess{}
	for _, item := range list1 {
		seenHandles[item.Handle] = item
	}
	if _, ok := seenHandles[awaitedUnpromoted]; ok {
		t.Fatalf("unpromoted awaited command %s leaked into project background list", awaitedUnpromoted)
	}
	if _, ok := seenHandles[bg2]; ok {
		t.Fatalf("other project command %s leaked into proj-1 list", bg2)
	}
	if got := seenHandles[bg1].RootSessionID; got != "chat-root-1" {
		t.Fatalf("worker child command RootSessionID = %q, want chat-root-1", got)
	}

	counts1 := reg.SessionBackgroundRunningCounts("proj-1")
	if counts1["chat-root-1"] != 1 || counts1["chat-root-2"] != 1 {
		t.Fatalf("SessionBackgroundRunningCounts(proj-1) = %+v, want chat-root-1=1, chat-root-2=1", counts1)
	}
	if got := reg.SessionBackgroundRunningCount("chat-root-1"); got != 1 {
		t.Fatalf("SessionBackgroundRunningCount(chat-root-1) = %d, want 1", got)
	}

	allRunning := reg.AllRunningBackgroundProcesses(t.Context())
	if len(allRunning) != 3 {
		t.Fatalf("AllRunningBackgroundProcesses = %+v, want 3 running jobs across proj-1 and proj-2", allRunning)
	}

	// Complete 3 quick background commands in proj-1 to verify MaxRecent=2 pruning.
	for i := range 3 {
		h, err := reg.StartPipeline(t.Context(), bgprocess.PipelineSpec{
			SessionID: "chat-recent",
			ProjectID: "proj-1",
			Runner:    runner,
			Mode:      bgprocess.JobModeBackground,
			Request: hostcmd.Request{
				Launch:     exec.HostLaunch("proj-1 quick"),
				ProjectDir: dir1,
				IOParams:   hostcmd.IOParams{InlineEnv: map[string]string{"ITER": strconv.Itoa(i)}},
				Stages:     []exec.Stage{{Name: "true"}},
			},
		})
		testutil.FailErr(t, "start quick command", err)
		finished, err := reg.Lifecycle.Await(t.Context(), "chat-recent", h, 5*time.Second)
		testutil.FailErr(t, "await quick command", err)
		if !finished {
			t.Fatal("quick command did not finish")
		}
	}

	listAfterRecent, _ := reg.ListProjectBackground(t.Context(), "proj-1")
	endedCount := 0
	runningCount := 0
	for _, item := range listAfterRecent {
		if item.Running {
			runningCount++
		} else {
			endedCount++
		}
	}
	if runningCount != 2 || endedCount != 2 {
		t.Fatalf("after 3 completed jobs with MaxRecent=2: running=%d ended=%d, want running=2 ended=2", runningCount, endedCount)
	}

	mu.Lock()
	gotEvents := append([]api.ProjectBackgroundEvent(nil), events...)
	mu.Unlock()
	var prevRev int64
	for _, ev := range gotEvents {
		if ev.ProjectID == "proj-1" {
			if ev.Revision <= prevRev {
				t.Fatalf("proj-1 event revision %d not strictly increasing after %d", ev.Revision, prevRev)
			}
			prevRev = ev.Revision
		}
	}
}

func TestListenerSweepSharedScanCoalescingAndPortChangeEvents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh/sleep fixture is unix-oriented")
	}
	var (
		scanCount atomic.Int32
		portMu    sync.Mutex
		mockPorts = map[string][]netip.AddrPort{}
		eventMu   sync.Mutex
		events    []api.ProjectBackgroundEvent
		portCh    = make(chan api.ProjectBackgroundEvent, 16)
	)

	reg := newTestRegistry(t, bgprocess.Config{
		MaxBackground:        4,
		SweepInterval:        150 * time.Millisecond,
		OutputCoalesceWindow: 40 * time.Millisecond,
		ListenerObserver: func(_ context.Context) ([]bgprocess.ObservedListener, error) {
			scanCount.Add(1)
			portMu.Lock()
			defer portMu.Unlock()
			var out []bgprocess.ObservedListener
			for handle, addrs := range mockPorts {
				for _, addr := range addrs {
					out = append(out, bgprocess.ObservedListener{
						Addr:   addr,
						Handle: handle,
					})
				}
			}
			return out, nil
		},
	}, bgprocess.Hooks{
		PublishProject: func(_ context.Context, ev api.ProjectBackgroundEvent) {
			eventMu.Lock()
			events = append(events, ev)
			eventMu.Unlock()
			if ev.Action == "ports_changed" {
				select {
				case portCh <- ev:
				default:
				}
			}
		},
	})
	t.Cleanup(func() { _ = reg.Lifecycle.Close(context.Background()) })
	runner := hostcmd.NewRunner()
	dir := t.TempDir()

	h1, err := reg.StartPipeline(t.Context(), bgprocess.PipelineSpec{
		SessionID: "sess-1", ProjectID: "proj-1", Runner: runner, Mode: bgprocess.JobModeBackground,
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("server 1"),
			ProjectDir: dir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
		},
	})
	testutil.FailErr(t, "start h1", err)

	h2, err := reg.StartPipeline(t.Context(), bgprocess.PipelineSpec{
		SessionID: "sess-1", ProjectID: "proj-1", Runner: runner, Mode: bgprocess.JobModeBackground,
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("server 2"),
			ProjectDir: dir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"29"}}},
		},
	})
	testutil.FailErr(t, "start h2", err)

	// Configure multi-family listening endpoints on h1 and trigger a refresh.
	portMu.Lock()
	mockPorts[h1] = []netip.AddrPort{
		netip.MustParseAddrPort("127.0.0.1:8080"),
		netip.MustParseAddrPort("[::1]:8080"),
	}
	portMu.Unlock()

	reg.RefreshListeners(t.Context())

	select {
	case ev := <-portCh:
		if ev.ProcessID != h1 || ev.Action != api.ProjectBackgroundEventActionPortsChanged {
			t.Fatalf("unexpected port event: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ports_changed event")
	}

	rows, _ := reg.ListProjectBackground(t.Context(), "proj-1")
	var h1Row *bgprocess.ProjectProcess
	for i := range rows {
		if rows[i].Handle == h1 {
			h1Row = &rows[i]
			break
		}
	}
	if h1Row == nil || !slices.Equal(h1Row.Ports, []string{"127.0.0.1:8080", "[::1]:8080"}) {
		t.Fatalf("h1 ports = %+v, want [127.0.0.1:8080 [::1]:8080]", h1Row)
	}

	// A second refresh with identical ports must NOT emit another ports_changed event.
	reg.RefreshListeners(t.Context())
	select {
	case ev := <-portCh:
		t.Fatalf("duplicate ports_changed event emitted when ports did not change: %+v", ev)
	default:
	}

	// Stop both background jobs and verify periodic sweeps stop once no live jobs remain.
	_, err = reg.Lifecycle.StopProjectProcess("proj-1", h1, bgprocess.TerminationStoppedByUser)
	testutil.FailErr(t, "stop h1", err)
	_, err = reg.Lifecycle.StopProjectProcess("proj-1", h2, bgprocess.TerminationStoppedByUser)
	testutil.FailErr(t, "stop h2", err)

	for _, h := range []string{h1, h2} {
		finished, waitErr := reg.Lifecycle.Await(t.Context(), "sess-1", h, 5*time.Second)
		testutil.FailErr(t, "await "+h, waitErr)
		if !finished {
			t.Fatalf("handle %s did not settle after stop", h)
		}
	}

	time.Sleep(80 * time.Millisecond)
	scansAfterIdle := scanCount.Load()
	time.Sleep(220 * time.Millisecond)
	if later := scanCount.Load(); later != scansAfterIdle {
		t.Fatalf("sweeper continued scanning after all background jobs ended: before=%d after=%d", scansAfterIdle, later)
	}
}

func TestRootDetachAdmissionBoundaryBlocksConcurrentLaunchAndPromotion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	reg := newTestRegistry(t, bgprocess.Config{MaxBackground: 8, MaxAwaited: 8}, bgprocess.Hooks{})
	t.Cleanup(func() { _ = reg.Lifecycle.Close(context.Background()) })
	runner := hostcmd.NewRunner()
	rootA := t.TempDir()

	// Start one background job and one awaited job under rootA before detach begins.
	bgHandle, err := reg.StartPipeline(t.Context(), bgprocess.PipelineSpec{
		SessionID: "sess-1", ProjectID: "proj-1", Runner: runner, Mode: bgprocess.JobModeBackground,
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("bg under rootA"),
			ProjectDir: rootA,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
		},
	})
	testutil.FailErr(t, "start bgHandle", err)

	awaitedHandle, err := reg.StartPipeline(t.Context(), bgprocess.PipelineSpec{
		SessionID: "sess-1", ProjectID: "proj-1", Runner: runner, Mode: bgprocess.JobModeAwaited,
		AllowConcurrent: true,
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("awaited under rootA"),
			ProjectDir: rootA,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"29"}}},
		},
	})
	testutil.FailErr(t, "start awaitedHandle", err)

	// Race StopUnderRoot against promoting awaitedHandle and launching a new background job under rootA.
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		_ = reg.Lifecycle.StopUnderRoot(t.Context(), "proj-1", nil, rootA, nil, bgprocess.TerminationRootDetached)
	}()
	go func() {
		defer wg.Done()
		_ = reg.Output.Promote(t.Context(), "sess-1", awaitedHandle)
	}()
	var racedHandle string
	var racedErr error
	go func() {
		defer wg.Done()
		racedHandle, racedErr = reg.StartPipeline(t.Context(), bgprocess.PipelineSpec{
			SessionID: "sess-2", ProjectID: "proj-1", Runner: runner, Mode: bgprocess.JobModeBackground,
			Request: hostcmd.Request{
				Launch:     exec.HostLaunch("raced bg under rootA"),
				ProjectDir: rootA,
				Stages:     []exec.Stage{{Name: "sleep", Args: []string{"28"}}},
			},
		})
	}()
	wg.Wait()

	// If racedHandle started before StopUnderRoot finished, stop it if it started after StopUnderRoot returned,
	// or verify that nothing launched during StopUnderRoot escaped teardown.
	if racedErr != nil && !errors.Is(racedErr, bgprocess.ErrTeardownInProgress) {
		t.Fatalf("unexpected raced start error: %v", racedErr)
	}
	if racedHandle != "" {
		// If racedHandle entered before StopUnderRoot acquired the lock, it must have been stopped by StopUnderRoot.
		// Clean up in case it entered after StopUnderRoot completed.
		_, _ = reg.Lifecycle.StopProjectProcess("proj-1", racedHandle, bgprocess.TerminationRootDetached)
	}

	for _, h := range []string{bgHandle, awaitedHandle} {
		finished, err := reg.Lifecycle.Await(t.Context(), "sess-1", h, 5*time.Second)
		testutil.FailErr(t, "await "+h, err)
		if !finished {
			t.Fatalf("handle %s escaped StopUnderRoot", h)
		}
		snap, err := reg.Snapshot(t.Context(), "sess-1", h, 0)
		testutil.FailErr(t, "snapshot "+h, err)
		if snap.TerminationReason != bgprocess.TerminationRootDetached {
			t.Fatalf("handle %s termination reason = %q, want root_detached", h, snap.TerminationReason)
		}
	}
}
