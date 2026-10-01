package bgprocess_test

import (
	"context"
	"errors"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

const backgroundCaptureSecret = "capture-secret-value"

func backgroundProjector() *captureprojection.Projector {
	m := secretmatch.NewInertMatcher()
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{
			Secret: backgroundCaptureSecret,
			RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle,
			Source: secretmatch.SourceRememberedMatch, NonDisclosable: true,
		}}
	})
	return captureprojection.New(m, nil)
}

func startBackground(
	ctx context.Context,
	reg *bgprocess.Registry,
	sessionID, projectID string,
	req hostcmd.Request,
	runner *hostcmd.Runner,
) (string, error) {
	if req.Launch.Kind == "" {
		req.Launch = exec.HostLaunch("bgprocess test")
	}
	return reg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: sessionID, ProjectID: projectID, Request: req,
		Runner: runner,
		Mode:   bgprocess.JobModeBackground,
	})
}

func TestBackgroundStartReturnsHandle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep-based fixture is unix-oriented")
	}
	reg := newTestRegistry(t, bgprocess.Config{MaxBackground: 4}, bgprocess.Hooks{})
	runner := hostcmd.NewRunner()
	handle, err := startBackground(context.Background(), reg, "sess-1", "proj-1", hostcmd.Request{Launch: exec.HostLaunch("bgprocess test"),
		ProjectDir: t.TempDir(),
		Stages:     []exec.Stage{{Name: "sleep", Args: []string{"2"}}},
	}, runner)
	testutil.FailErr(t, "start background", err)
	if strings.TrimSpace(handle) == "" {
		t.Fatal("expected non-empty handle")
	}
	if known, running := reg.State("sess-1", handle); !known || !running {
		t.Fatalf("owned process state = known:%v running:%v", known, running)
	}
	if known, _ := reg.State("other-session", handle); known {
		t.Fatal("process state crossed session ownership")
	}
	if known, _ := reg.State("sess-1", "missing"); known {
		t.Fatal("unknown process reported as owned")
	}
	out, err := reg.ReadOutput(context.Background(), "sess-1", handle)
	testutil.FailErr(t, "output after start", err)
	if !out.Running {
		t.Fatal("expected process running immediately after start")
	}
}

func TestBackgroundCommandLineUsesRecordedStages(t *testing.T) {
	reg := newTestRegistry(t, bgprocess.DefaultConfig(), bgprocess.Hooks{})
	runner := hostcmd.NewRunner()
	handle, err := startBackground(context.Background(), reg, "sess-1", "proj-1", hostcmd.Request{Launch: exec.HostLaunch("bgprocess test"),
		ProjectDir: t.TempDir(),
		Stages: []exec.Stage{
			{Name: "printf", Args: []string{"hello"}},
			{Name: "cat"},
		},
	}, runner)
	testutil.FailErr(t, "start background", err)
	command, err := reg.CommandLine("sess-1", handle)
	testutil.FailErr(t, "describe background command", err)
	if command != "printf hello | cat" {
		t.Fatalf("command = %q want %q", command, "printf hello | cat")
	}
	stopped, err := reg.Stop("sess-1", handle)
	testutil.FailErr(t, "stop background", err)
	if !stopped.StopRequested || (stopped.Running && stopped.ExitCode != nil) {
		t.Fatalf("stop request misstates terminal state: %+v", stopped)
	}
}

func TestBackgroundOutputStreamsIncrementally(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh fixture is unix-oriented")
	}
	reg := newTestRegistry(t, bgprocess.Config{MaxBackground: 4}, bgprocess.Hooks{})
	runner := hostcmd.NewRunner()
	handle, err := startBackground(context.Background(), reg, "sess-1", "proj-1", hostcmd.Request{Launch: exec.HostLaunch("bgprocess test"),
		ProjectDir: t.TempDir(),
		Stages:     []exec.Stage{{Name: "sh", Args: []string{"-c", "printf line1; sleep 0.2; printf line2"}}},
	}, runner)
	testutil.FailErr(t, "start background", err)

	deadline := time.Now().Add(3 * time.Second)
	var first bgprocess.OutputSnapshot
	var firstFound, secondFound bool
	for time.Now().Before(deadline) {
		snapshot, err := reg.ReadRawOutput("sess-1", handle, 0)
		testutil.FailErr(t, "command_output cursor 0", err)
		if len(snapshot.Output.Chunks) > 0 {
			first = snapshot
			firstFound = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !firstFound {
		t.Fatal("expected incremental output")
	}
	cursor := first.Output.Next
	for time.Now().Before(deadline) {
		snapshot, err := reg.ReadRawOutput("sess-1", handle, cursor)
		testutil.FailErr(t, "command_output follow-up", err)
		if len(snapshot.Output.Chunks) > 0 {
			secondFound = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !secondFound {
		t.Fatal("expected output after cursor")
	}
}

func TestBackgroundLiveAndReloadProjectionCatchSecretsSplitAcrossWrites(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh fixture is unix-oriented")
	}
	events := make(chan api.BackgroundProcessEvent, 16)
	reg := newTestRegistry(t, bgprocess.DefaultConfig(), bgprocess.Hooks{
		Publish: func(_ context.Context, _, _ string, event api.BackgroundProcessEvent) {
			events <- event
		},
	})
	reg.SetCaptureProjector(backgroundProjector())
	handle, err := startBackground(t.Context(), reg, "sess-1", "proj-1", hostcmd.Request{
		ProjectDir: t.TempDir(),
		Stages: []exec.Stage{{
			Name: "sh",
			Args: []string{"-c", "printf 'capture-'; sleep 0.2; printf 'secret-value'"},
		}},
	}, hostcmd.NewRunner())
	testutil.FailErr(t, "start background", err)
	finished, err := reg.Await(t.Context(), "sess-1", handle, 5*time.Second)
	testutil.FailErr(t, "await background", err)
	if !finished {
		t.Fatal("background process did not finish")
	}

	var live string
	deadline := time.After(500 * time.Millisecond)
	drain := true
	for drain {
		select {
		case event := <-events:
			if strings.Contains(event.Text, backgroundCaptureSecret) {
				t.Fatalf("live event exposed secret: %+v", event)
			}
			if event.Reset {
				live = event.Text
			} else {
				live += event.Text
			}
		case <-deadline:
			drain = false
		}
	}
	if strings.Contains(live, backgroundCaptureSecret) || !strings.Contains(live, "[REDACTED]") {
		t.Fatalf("live projection = %q", live)
	}

	// Rebuild from the full window to catch split values.
	reloaded, err := reg.ReadOutput(context.Background(), "sess-1", handle)
	testutil.FailErr(t, "read projected output", err)
	var text strings.Builder
	for _, chunk := range reloaded.Chunks {
		text.WriteString(chunk.Text)
	}
	if strings.Contains(text.String(), backgroundCaptureSecret) || !strings.Contains(text.String(), "[REDACTED]") {
		t.Fatalf("reload projection = %q", text.String())
	}
}

func TestBackgroundListProjectsSecretBearingCommandLine(t *testing.T) {
	reg := newTestRegistry(t, bgprocess.DefaultConfig(), bgprocess.Hooks{})
	reg.SetCaptureProjector(backgroundProjector())
	handle, err := startBackground(t.Context(), reg, "sess-1", "proj-1", hostcmd.Request{
		ProjectDir: t.TempDir(),
		Stages:     []exec.Stage{{Name: "printf", Args: []string{backgroundCaptureSecret}}},
	}, hostcmd.NewRunner())
	testutil.FailErr(t, "start background", err)
	_, err = reg.Await(t.Context(), "sess-1", handle, 5*time.Second)
	testutil.FailErr(t, "await background", err)
	listed := reg.List(context.Background(), "sess-1")
	if len(listed) != 1 || len(listed[0].Stages) != 1 {
		t.Fatalf("listed processes = %+v", listed)
	}
	if command := listed[0].Stages[0].Command; strings.Contains(command, backgroundCaptureSecret) || !strings.Contains(command, "[REDACTED]") {
		t.Fatalf("projected command = %q", command)
	}
}

func TestBackgroundStopKillsProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	reg := newTestRegistry(t, bgprocess.Config{MaxBackground: 4}, bgprocess.Hooks{})
	runner := hostcmd.NewRunner()
	handle, err := startBackground(context.Background(), reg, "sess-1", "proj-1", hostcmd.Request{Launch: exec.HostLaunch("bgprocess test"),
		ProjectDir: t.TempDir(),
		Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
	}, runner)
	testutil.FailErr(t, "start background", err)
	_, err = reg.Stop("sess-1", handle)
	testutil.FailErr(t, "stop background", err)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		out, err := reg.ReadOutput(context.Background(), "sess-1", handle)
		testutil.FailErr(t, "output after stop", err)
		if !out.Running {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("expected process stopped")
}

func TestBackgroundSessionEndKillsAll(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	reg := newTestRegistry(t, bgprocess.Config{MaxBackground: 4}, bgprocess.Hooks{})
	runner := hostcmd.NewRunner()
	_, err := startBackground(context.Background(), reg, "sess-1", "proj-1", hostcmd.Request{Launch: exec.HostLaunch("bgprocess test"),
		ProjectDir: t.TempDir(),
		Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
	}, runner)
	testutil.FailErr(t, "start background", err)
	testutil.FailErr(t, "dispose session processes", reg.DisposeSession(context.Background(), "sess-1"))
	list := reg.List(context.Background(), "sess-1")
	if len(list) != 0 {
		t.Fatal("expected session end to remove background processes")
	}
}

func TestRegistryCloseStopsAllSessionsAndRejectsNewStarts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	reg := newTestRegistry(t, bgprocess.Config{MaxBackground: 4}, bgprocess.Hooks{})
	runner := hostcmd.NewRunner()
	for _, sessionID := range []string{"sess-1", "sess-2"} {
		_, err := startBackground(t.Context(), reg, sessionID, "proj-1", hostcmd.Request{Launch: exec.HostLaunch("bgprocess test"),
			ProjectDir: t.TempDir(),
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
		}, runner)
		testutil.FailErr(t, "start process for "+sessionID, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	testutil.FailErr(t, "close registry", reg.Close(ctx))
	if got := len(reg.List(context.Background(), "sess-1")) + len(reg.List(context.Background(), "sess-2")); got != 0 {
		t.Fatalf("processes retained after close = %d", got)
	}
	_, err := startBackground(t.Context(), reg, "sess-3", "proj-1", hostcmd.Request{Launch: exec.HostLaunch("bgprocess test"),
		ProjectDir: t.TempDir(),
		Stages:     []exec.Stage{{Name: "true"}},
	}, runner)
	if !errors.Is(err, bgprocess.ErrRegistryClosed) {
		t.Fatalf("start after close = %v, want ErrRegistryClosed", err)
	}
}

func TestBackgroundCapReached(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	reg := newTestRegistry(t, bgprocess.Config{MaxBackground: 1}, bgprocess.Hooks{})
	runner := hostcmd.NewRunner()
	req := hostcmd.Request{Launch: exec.HostLaunch("bgprocess test"),
		ProjectDir: t.TempDir(),
		Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
	}
	handle, err := startBackground(context.Background(), reg, "sess-1", "proj-1", req, runner)
	testutil.FailErr(t, "first background start", err)
	req.Stages[0].Args = []string{"29"}
	_, err = startBackground(context.Background(), reg, "sess-1", "proj-1", req, runner)
	if err == nil {
		t.Fatal("expected cap reject")
	}
	var capacity *bgprocess.BackgroundCapacityError
	if !errors.Is(err, bgprocess.ErrBackgroundCapReached) || !errors.As(err, &capacity) {
		t.Fatalf("expected cap error, got %v", err)
	}
	if capacity.Limit != 1 || len(capacity.TerminalIDs) != 0 || len(capacity.CommandHandles) != 1 || capacity.CommandHandles[0] != handle {
		t.Fatalf("capacity snapshot = %+v", capacity)
	}
}

func TestCompletedHandleDoesNotOccupyRunningCap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("true/sleep fixture is unix-oriented")
	}
	reg := newTestRegistry(t, bgprocess.Config{MaxBackground: 1}, bgprocess.Hooks{})
	runner := hostcmd.NewRunner()
	projectDir := t.TempDir()
	handle, err := startBackground(context.Background(), reg, "sess-1", "proj-1", hostcmd.Request{Launch: exec.HostLaunch("bgprocess test"),
		ProjectDir: projectDir,
		Stages:     []exec.Stage{{Name: "true"}},
	}, runner)
	testutil.FailErr(t, "start quick background command", err)
	finished, err := reg.Await(context.Background(), "sess-1", handle, 5*time.Second)
	testutil.FailErr(t, "await quick background command", err)
	if !finished {
		t.Fatal("quick background command did not finish")
	}
	if len(reg.ActiveJobs("sess-1")) != 0 {
		t.Fatal("completed handle must not remain an active job")
	}
	if !reg.HasPipelineHandles("sess-1") {
		t.Fatal("completed handle must still be inspectable")
	}
	_, err = startBackground(context.Background(), reg, "sess-1", "proj-1", hostcmd.Request{Launch: exec.HostLaunch("bgprocess test"),
		ProjectDir: projectDir,
		Stages:     []exec.Stage{{Name: "sleep", Args: []string{"1"}}},
	}, runner)
	testutil.FailErr(t, "start after completed handle", err)
}

func TestCompletedHandleRetentionIsBounded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("true fixture is unix-oriented")
	}
	reg := newTestRegistry(t, bgprocess.Config{MaxBackground: 1, MaxRecent: 2}, bgprocess.Hooks{})
	runner := hostcmd.NewRunner()
	projectDir := t.TempDir()
	for i := range 3 {
		handle, err := startBackground(context.Background(), reg, "sess-1", "proj-1", hostcmd.Request{Launch: exec.HostLaunch("bgprocess test"),
			ProjectDir: projectDir,
			IOParams: hostcmd.IOParams{
				InlineEnv: map[string]string{"RUN": strconv.Itoa(i)},
			},
			Stages: []exec.Stage{{Name: "true"}},
		}, runner)
		testutil.FailErr(t, "start retained command", err)
		finished, err := reg.Await(context.Background(), "sess-1", handle, 5*time.Second)
		testutil.FailErr(t, "await retained command", err)
		if !finished {
			t.Fatal("retained command did not finish")
		}
	}
	if got := len(reg.List(context.Background(), "sess-1")); got != 2 {
		t.Fatalf("retained completed handles = %d want 2", got)
	}
}

func TestBackgroundStartRejectsMetachar(t *testing.T) {
	reg := newTestRegistry(t, bgprocess.DefaultConfig(), bgprocess.Hooks{})
	runner := hostcmd.NewRunner()
	_, err := startBackground(context.Background(), reg, "sess-1", "proj-1", hostcmd.Request{Launch: exec.HostLaunch("bgprocess test"),
		ProjectDir: t.TempDir(),
		Stages:     []exec.Stage{{Name: "echo;", Args: []string{"x"}}},
	}, runner)
	if err == nil {
		t.Fatal("expected metachar reject before launch")
	}
}

func TestCommandAdmissionRejectsLiveDuplicateBeforeConcurrency(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	reg := newTestRegistry(t, bgprocess.DefaultConfig(), bgprocess.Hooks{})
	t.Cleanup(func() {
		testutil.FailErr(t, "dispose session processes", reg.DisposeSession(context.Background(), "sess-1"))
	})
	runner := hostcmd.NewRunner()
	req := hostcmd.Request{Launch: exec.HostLaunch("bgprocess test"),
		ProjectDir: t.TempDir(),
		Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
	}
	_, err := reg.StartPipeline(context.Background(), bgprocess.PipelineSpec{
		SessionID: "sess-1", Request: req, Runner: runner, Mode: bgprocess.JobModeBackground,
	})
	testutil.FailErr(t, "start first command", err)
	_, err = reg.StartPipeline(context.Background(), bgprocess.PipelineSpec{
		SessionID: "sess-1", Request: req, Runner: runner, Mode: bgprocess.JobModeAwaited,
		AllowConcurrent: true,
	})
	if !errors.Is(err, bgprocess.ErrDuplicateRunning) {
		t.Fatalf("duplicate start error = %v, want ErrDuplicateRunning", err)
	}
}

func TestCommandAdmissionRequiresExplicitAwaitedConcurrency(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	reg := newTestRegistry(t, bgprocess.DefaultConfig(), bgprocess.Hooks{})
	t.Cleanup(func() {
		testutil.FailErr(t, "dispose session processes", reg.DisposeSession(context.Background(), "sess-1"))
	})
	runner := hostcmd.NewRunner()
	start := func(seconds string, allow bool) (string, error) {
		return reg.StartPipeline(context.Background(), bgprocess.PipelineSpec{
			SessionID: "sess-1", Runner: runner, Mode: bgprocess.JobModeAwaited,
			AllowConcurrent: allow,
			Request: hostcmd.Request{Launch: exec.HostLaunch("bgprocess test"),
				ProjectDir: t.TempDir(),
				Stages:     []exec.Stage{{Name: "sleep", Args: []string{seconds}}},
			},
		})
	}
	_, err := start("30", false)
	testutil.FailErr(t, "start first awaited command", err)
	_, err = start("29", false)
	if !errors.Is(err, bgprocess.ErrCommandInFlight) {
		t.Fatalf("second awaited start error = %v, want ErrCommandInFlight", err)
	}
	_, err = start("28", true)
	testutil.FailErr(t, "start explicitly concurrent awaited command", err)
}

func TestAwaitedConcurrencyHasIndependentHardCap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	reg := newTestRegistry(t, bgprocess.Config{MaxAwaited: 2}, bgprocess.Hooks{})
	t.Cleanup(func() {
		testutil.FailErr(t, "dispose session processes", reg.DisposeSession(context.Background(), "sess-1"))
	})
	runner := hostcmd.NewRunner()
	projectDir := t.TempDir()
	start := func(seconds string) error {
		_, err := reg.StartPipeline(context.Background(), bgprocess.PipelineSpec{
			SessionID: "sess-1", Runner: runner, Mode: bgprocess.JobModeAwaited,
			AllowConcurrent: true,
			Request: hostcmd.Request{Launch: exec.HostLaunch("bgprocess test"),
				ProjectDir: projectDir,
				Stages:     []exec.Stage{{Name: "sleep", Args: []string{seconds}}},
			},
		})
		return err
	}
	testutil.FailErr(t, "start first explicitly concurrent command", start("30"))
	testutil.FailErr(t, "start second explicitly concurrent command", start("29"))
	if err := start("28"); !errors.Is(err, bgprocess.ErrAwaitedCapReached) {
		t.Fatalf("third awaited start error = %v want ErrAwaitedCapReached", err)
	}
}

func TestExecutionDeadlinePublishesOneTerminalCompletion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	completed := make(chan bgprocess.Completion, 2)
	reg := newTestRegistry(t, bgprocess.DefaultConfig(), bgprocess.Hooks{
		Complete: func(_ context.Context, completion bgprocess.Completion) { completed <- completion },
	})
	runner := hostcmd.NewRunner()
	handle, err := reg.StartPipeline(context.Background(), bgprocess.PipelineSpec{
		SessionID: "sess-1", Runner: runner, Mode: bgprocess.JobModeBackground,
		OriginTool: "verify", Timeout: 50 * time.Millisecond,
		Request: hostcmd.Request{Launch: exec.HostLaunch("bgprocess test"),
			ProjectDir: t.TempDir(),
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
		},
	})
	testutil.FailErr(t, "start deadline command", err)
	finished, err := reg.Await(context.Background(), "sess-1", handle, 5*time.Second)
	testutil.FailErr(t, "await deadline command", err)
	if !finished {
		t.Fatal("deadline command did not finish")
	}
	select {
	case completion := <-completed:
		if completion.TerminationReason != bgprocess.TerminationTimedOut {
			t.Fatalf("termination = %q want timed_out", completion.TerminationReason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal completion was not published")
	}
	select {
	case completion := <-completed:
		t.Fatalf("duplicate terminal completion: %#v", completion)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestPromotePublishesTerminalCompletionAfterExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	completed := make(chan bgprocess.Completion, 2)
	reg := newTestRegistry(t, bgprocess.DefaultConfig(), bgprocess.Hooks{
		Complete: func(_ context.Context, completion bgprocess.Completion) { completed <- completion },
	})
	runner := hostcmd.NewRunner()
	handle, err := reg.StartPipeline(context.Background(), bgprocess.PipelineSpec{
		SessionID: "sess-1", Runner: runner, Mode: bgprocess.JobModeAwaited,
		Request: hostcmd.Request{Launch: exec.HostLaunch("bgprocess test"),
			ProjectDir: t.TempDir(),
			Stages:     []exec.Stage{{Name: "true"}},
		},
	})
	testutil.FailErr(t, "start quick awaited command", err)
	finished, err := reg.Await(context.Background(), "sess-1", handle, 5*time.Second)
	testutil.FailErr(t, "await quick command", err)
	if !finished {
		t.Fatal("quick command did not finish")
	}
	select {
	case <-completed:
		t.Fatal("unpromoted awaited command must stay invisible")
	default:
	}
	testutil.FailErr(t, "promote completed command", reg.Promote(context.Background(), "sess-1", handle))
	select {
	case <-completed:
	case <-time.After(2 * time.Second):
		t.Fatal("promotion did not publish terminal completion")
	}
	select {
	case completion := <-completed:
		t.Fatalf("duplicate promoted completion: %#v", completion)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRingBufferTruncation(t *testing.T) {
	buf := bgprocess.NewRingBuffer(16)
	buf.Append("stdout", []byte("0123456789abcdef"))
	buf.Append("stdout", []byte("extra"))
	// The following append releases the overflow from the window.
	buf.Append("stdout", []byte("!"))
	_, _, truncated := buf.ReadSince(0)
	if !truncated {
		t.Fatal("expected truncated flag after ring overflow")
	}
}

// TestRingBufferRenderNamesEachStreamRunOnce keeps adjacent chunks joined.
func TestRingBufferRenderNamesEachStreamRunOnce(t *testing.T) {
	buf := bgprocess.NewRingBuffer(1 << 20)
	buf.Append("stdout", []byte("first"))
	buf.Append("stdout", []byte("second"))
	buf.Append("stderr", []byte("third"))
	got := buf.Render()
	want := "stdout: firstsecond\nstderr: third"
	if got != want {
		t.Fatalf("render = %q want %q", got, want)
	}
}

func TestCutTailKeepsTheNewestBytes(t *testing.T) {
	got := bgprocess.CutTail("stdout: aaaaaaaaaabbbbbbbbbb", 10)
	if got != "bbbbbbbbbb" {
		t.Fatalf("cut tail = %q want the newest bytes", got)
	}
}

func TestBackgroundLaunchFailureIsOnTheJobResult(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("true fixture is unix-oriented")
	}
	completed := make(chan bgprocess.Completion, 1)
	reg := newTestRegistry(t, bgprocess.DefaultConfig(), bgprocess.Hooks{
		Complete: func(_ context.Context, completion bgprocess.Completion) { completed <- completion },
	})
	stages, err := exec.StagesFromCommandLine("true && ./no-such-binary-for-launch")
	testutil.FailErr(t, "parse stages", err)
	handle, err := startBackground(context.Background(), reg, "sess-1", "proj-1", hostcmd.Request{
		ProjectDir: t.TempDir(), Stages: stages,
	}, hostcmd.NewRunner())
	testutil.FailErr(t, "start command", err)
	finished, err := reg.Await(context.Background(), "sess-1", handle, 5*time.Second)
	testutil.FailErr(t, "await command", err)
	if !finished {
		t.Fatal("command did not finish")
	}

	snap, err := reg.Snapshot(context.Background(), "sess-1", handle, 0)
	testutil.FailErr(t, "snapshot", err)
	if snap.Failure == nil || snap.Failure.Kind != hostcmd.ExecFailureStageLaunch ||
		!strings.Contains(snap.Failure.Detail, "no-such-binary-for-launch") {
		t.Fatalf("snapshot failure = %+v, want the stage launch error", snap.Failure)
	}
	raw, err := reg.ReadRawOutput("sess-1", handle, 0)
	testutil.FailErr(t, "read raw output", err)
	if raw.Failure == nil || raw.Failure.Kind != hostcmd.ExecFailureStageLaunch {
		t.Fatalf("output failure = %+v, want the stage launch error", raw.Failure)
	}
	select {
	case completion := <-completed:
		if completion.Failure == nil || completion.Failure.Kind != hostcmd.ExecFailureStageLaunch {
			t.Fatalf("completion failure = %+v, want the stage launch error", completion.Failure)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal completion was not published")
	}
}
