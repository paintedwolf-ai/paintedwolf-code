package bgprocess

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	lycexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/indexwatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

type controlledDrainJob struct {
	handle   string
	canceled chan struct{}
	release  func()
}

func awaitDrainSignal(t *testing.T, step string, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: lifecycle signal did not arrive", step)
	}
}

func startControlledDrainJob(t *testing.T, registry *Registry, session string) controlledDrainJob {
	t.Helper()
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	t.Cleanup(finish)
	handle, err := registry.StartProgram(t.Context(), ProgramJobSpec{
		SessionID: session, CommandLine: "owned controlled job",
		Run: func(ctx context.Context, _, _ io.Writer) (ProgramJobResult, error) {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
			return ProgramJobResult{ExitCode: 130}, ctx.Err()
		},
	})
	testutil.FailErr(t, "start actual controlled program job", err)
	awaitDrainSignal(t, "actual job start", started)
	return controlledDrainJob{handle: handle, canceled: canceled, release: finish}
}

func assertDrainPending(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		t.Fatalf("disposal returned before physical job exit: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
}

func assertDrainAdmissionSealed(t *testing.T, registry *Registry, session string) {
	t.Helper()
	binary, err := os.Executable()
	testutil.FailErr(t, "locate admission fixture binary", err)
	request := hostcmd.Request{Launch: lycexec.HostLaunch("drain admission fixture"), ProjectDir: t.TempDir(),
		Stages: []lycexec.Stage{{Name: binary, Args: []string{"-test.run=^$"}}}}
	checks := map[string]func() error{
		"program": func() error {
			_, err := registry.StartProgram(t.Context(), ProgramJobSpec{SessionID: session,
				Run: func(context.Context, io.Writer, io.Writer) (ProgramJobResult, error) {
					t.Error("closed program admission executed a body")
					return ProgramJobResult{}, nil
				}})
			return err
		},
		"pipeline": func() error {
			_, err := registry.StartPipeline(t.Context(), PipelineSpec{SessionID: session, Request: request,
				Runner: hostcmd.NewRunner(), Mode: JobModeBackground})
			return err
		},
		"PTY": func() error {
			_, err := registry.Terminal.StartPTY(t.Context(), session, session, "project", request,
				hostcmd.NewRunner(), lycexec.WinSize{}, confine.SpawnFacts{})
			return err
		},
	}
	for name, start := range checks {
		if err := start(); !errors.Is(err, ErrRegistryClosed) {
			t.Fatalf("%s admission while drain pending = %v", name, err)
		}
	}
}

func TestDisposeSessionRetryWaitsForPhysicalJobs(t *testing.T) {
	registry := NewRegistry(Config{MaxBackground: 1}, Hooks{})
	t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })
	owned := startControlledDrainJob(t, registry, "owned")
	sibling := startControlledDrainJob(t, registry, "sibling")
	dir := t.TempDir()
	gittest.Init(t, dir)
	testutil.FailErr(t, "write indexed fixture", os.WriteFile(filepath.Join(dir, "file"), []byte("retained"), 0600))
	gittest.CommitAll(t, dir, "Capture fixture")
	capture := indexwatch.Take(dir)
	t.Cleanup(capture.Release)
	if !capture.Active() {
		t.Fatal("actual index capture was not created")
	}
	registry.Lifecycle.WatchIndex("owned", owned.handle, capture)
	proc, err := registry.jobs.lookup("owned", owned.handle)
	testutil.FailErr(t, "resolve original physical job", err)
	registry.jobs.mu.Lock()
	proc.boundary.HostExecution = true
	registry.jobs.mu.Unlock()
	short, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := registry.Lifecycle.DisposeSession(short, "owned"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("initial disposal = %v", err)
	}
	awaitDrainSignal(t, "owned cancellation acknowledged", owned.canceled)
	if _, err := registry.CommandLine("owned", owned.handle); !errors.Is(err, ErrProcessNotFound) || len(registry.Output.List(t.Context(), "owned")) != 0 {
		t.Fatal("disposing handles remained publicly addressable")
	}
	if !registry.HasRunningHandles("owned", []string{owned.handle}) || !capture.Active() {
		t.Fatal("timeout released physical job or index custody")
	}
	registry.jobs.mu.Lock()
	count := registry.jobs.countRunningBackgroundLocked("owned")
	capacity := registry.jobs.backgroundCapacityErrorLocked("owned")
	registry.jobs.mu.Unlock()
	if count != 1 || len(capacity.CommandHandles) != 1 || capacity.CommandHandles[0] != owned.handle {
		t.Fatal("hidden live job stopped counting against capacity")
	}
	if protection := registry.ActiveExecutionJobs("owned"); len(protection) != 1 || protection[0].Handle != owned.handle {
		t.Fatal("timeout released exceptional execution protection")
	}
	assertDrainAdmissionSealed(t, registry, "owned")
	select {
	case <-sibling.canceled:
		t.Fatal("session disposal canceled its sibling")
	default:
	}
	if !registry.HasRunning("sibling") {
		t.Fatal("sibling physical ownership disappeared")
	}
	onExit := make(chan struct{})
	registry.Lifecycle.OnExit("owned", owned.handle, func() { close(onExit) })
	long, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	retry := make(chan error, 1)
	go func() { retry <- registry.Lifecycle.DisposeSession(long, "owned") }()
	assertDrainPending(t, retry)
	select {
	case <-onExit:
		t.Fatal("hidden live job ran its exit callback early")
	default:
	}
	owned.release()
	testutil.FailErr(t, "retry actual physical drain", testutil.Receive(t, "retry completion", retry))
	awaitDrainSignal(t, "actual job exit callback", onExit)
	if capture.Active() || registry.HasRunning("owned") {
		t.Fatal("completed disposal retained physical resources")
	}
	registry.jobs.mu.Lock()
	retained := registry.jobs.drains["owned"]
	registry.jobs.mu.Unlock()
	if retained != nil {
		t.Fatal("completed drain retained a session tombstone")
	}
	_, err = registry.StartProgram(t.Context(), ProgramJobSpec{SessionID: "owned", Run: func(context.Context, io.Writer, io.Writer) (ProgramJobResult, error) { return ProgramJobResult{}, nil }})
	testutil.FailErr(t, "new admission after completed physical disposal", err)
}

func TestCloseRetryJoinsPendingSessionDrains(t *testing.T) {
	registry := NewRegistry(Config{}, Hooks{})
	t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })
	first := startControlledDrainJob(t, registry, "first")
	second := startControlledDrainJob(t, registry, "second")
	short, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := registry.Lifecycle.DisposeSession(short, "first"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first session disposal = %v", err)
	}
	shortClose, stopClose := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer stopClose()
	if err := registry.Lifecycle.Close(shortClose); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("initial global close = %v", err)
	}
	awaitDrainSignal(t, "first cancellation", first.canceled)
	awaitDrainSignal(t, "second cancellation", second.canceled)
	assertDrainAdmissionSealed(t, registry, "third")
	long, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	retry := make(chan error, 1)
	go func() { retry <- registry.Lifecycle.Close(long) }()
	assertDrainPending(t, retry)
	first.release()
	assertDrainPending(t, retry)
	if !registry.HasRunning("second") {
		t.Fatal("global retry discarded the remaining physical job")
	}
	second.release()
	testutil.FailErr(t, "global close retry", testutil.Receive(t, "global drain completion", retry))
	if registry.HasRunning("first") || registry.HasRunning("second") {
		t.Fatal("global close returned before physical completion")
	}
	assertDrainAdmissionSealed(t, registry, "third")
	registry.jobs.mu.Lock()
	pending := len(registry.jobs.drains)
	registry.jobs.mu.Unlock()
	if pending != 0 {
		t.Fatal("finished global close retained drain records")
	}
}
