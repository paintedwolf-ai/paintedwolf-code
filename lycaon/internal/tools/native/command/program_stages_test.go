package command

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

// stageLease reports refusals the test records, as a kernel feed would.
type stageLease struct {
	mu        sync.Mutex
	refusals  []confine.SandboxRefusal
	listeners []func()
}

func (l *stageLease) Refusals() confine.SandboxRefusals {
	l.mu.Lock()
	defer l.mu.Unlock()
	return confine.SandboxRefusals{Witness: confine.WitnessKernel, Refusals: append([]confine.SandboxRefusal(nil), l.refusals...)}
}

func (l *stageLease) OnRefusal(fn func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.listeners = append(l.listeners, fn)
}

func (l *stageLease) refuse(target string) {
	l.mu.Lock()
	l.refusals = append(l.refusals, confine.SandboxRefusal{Operation: "network-bind", Target: target, Count: 1, Recovery: confine.RecoverHostExecution})
	listeners := append([]func(){}, l.listeners...)
	l.mu.Unlock()
	for _, listen := range listeners {
		listen()
	}
}

func confinedStage(lease bgprocess.StageLease) bgprocess.ProgramStage {
	return bgprocess.ProgramStage{Boundary: confine.Boundary{Applied: true}, Lease: lease}
}

// continueAsJob binds tracker to a program job that runs until the test ends.
func continueAsJob(t *testing.T, registry *bgprocess.Registry, tracker *stageTracker) string {
	t.Helper()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	handle, err := tracker.bind(func(history bgprocess.ProgramHistory) (string, error) {
		return registry.StartProgram(t.Context(), bgprocess.ProgramJobSpec{
			SessionID: "session", CommandLine: "colima start", OriginTool: "command", ToolCallID: "call", History: history,
			Run: func(ctx context.Context, _, _ io.Writer) (bgprocess.ProgramJobResult, error) {
				select {
				case <-release:
				case <-ctx.Done():
				}
				return bgprocess.ProgramJobResult{}, nil
			},
		})
	}, func(handle string) func(bgprocess.ProgramStage) stageSettler {
		return func(stage bgprocess.ProgramStage) stageSettler {
			return registry.AttachProgramStage(context.WithoutCancel(t.Context()), "session", handle, stage)
		}
	})
	testutil.FailErr(t, "continue program as a job", err)
	return handle
}

func refusalNotices() (bgprocess.Hooks, chan bgprocess.RefusalNotice) {
	notices := make(chan bgprocess.RefusalNotice, 8)
	return bgprocess.Hooks{Refused: func(_ context.Context, notice bgprocess.RefusalNotice) { notices <- notice }}, notices
}

func nextNotice(t *testing.T, notices chan bgprocess.RefusalNotice) bgprocess.RefusalNotice {
	t.Helper()
	select {
	case notice := <-notices:
		return notice
	case <-t.Context().Done():
		t.Fatal("no refusal notice arrived")
		return bgprocess.RefusalNotice{}
	}
}

func TestContinuedProgramCarriesItsForegroundStages(t *testing.T) {
	hooks, notices := refusalNotices()
	registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), hooks)
	tracker := &stageTracker{}
	ctx := withStageTracker(t.Context(), tracker)

	settled := &stageLease{}
	settleFirst := attachProgramStage(ctx, confinedStage(settled))
	settled.refuse("/Users/person/.lima/colima/lima.sock")
	settleFirst(settled.Refusals(), []confine.EgressHost{{Host: "github.com", Port: 443}})

	running := &stageLease{}
	settleRunning := attachProgramStage(ctx, confinedStage(running))
	running.refuse("/Users/person/.lima/colima/ha.sock")

	handle := continueAsJob(t, registry, tracker)

	notice := nextNotice(t, notices)
	if notice.Handle != handle || notice.Unshown != 1 {
		t.Fatalf("running stage refusal notice = %+v, want one unshown refusal on %s", notice, handle)
	}
	snapshot, err := registry.Output.ReadRawOutput("session", handle, 0)
	testutil.FailErr(t, "read job output", err)
	if !snapshot.Confinement.Boundary.Applied {
		t.Fatal("continued job reported confined: false")
	}
	if got := snapshot.Confinement.Refusals().Refusals; len(got) != 2 {
		t.Fatalf("job refusals = %+v, want the settled and the running stage's", got)
	}
	if got := snapshot.Confinement.Network(); len(got) != 1 {
		t.Fatalf("job network = %+v, want the settled stage's", got)
	}

	settleRunning(running.Refusals(), nil)
	settleRunning(confine.SandboxRefusals{}, nil)
	snapshot, err = registry.Output.ReadRawOutput("session", handle, 0)
	testutil.FailErr(t, "read job output after settle", err)
	if got := snapshot.Confinement.Refusals().Refusals; len(got) != 2 {
		t.Fatalf("settling the handed-over stage lost refusals: %+v", got)
	}
}

func TestStagesAfterContinuationAttachToTheJob(t *testing.T) {
	hooks, notices := refusalNotices()
	registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), hooks)
	tracker := &stageTracker{}
	ctx := withStageTracker(t.Context(), tracker)
	handle := continueAsJob(t, registry, tracker)

	later := &stageLease{}
	attachProgramStage(ctx, confinedStage(later))
	later.refuse("/dev/bpf0")
	if notice := nextNotice(t, notices); notice.Handle != handle || notice.Unshown != 1 {
		t.Fatalf("later stage notice = %+v", notice)
	}
}

func TestSettledStagesBeforeContinuationAreNotNoticedAgain(t *testing.T) {
	hooks, _ := refusalNotices()
	registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), hooks)
	tracker := &stageTracker{}
	ctx := withStageTracker(t.Context(), tracker)
	lease := &stageLease{}
	settle := attachProgramStage(ctx, confinedStage(lease))
	lease.refuse("/Users/person/.lima/colima/ha.sock")
	settle(lease.Refusals(), nil)

	handle := continueAsJob(t, registry, tracker)
	if unshown := registry.Output.UnshownRefusals("session", handle); unshown != 0 {
		t.Fatalf("refusals the promoting result showed are unshown: %d", unshown)
	}
}

func heldRefusalSignal(t *testing.T, tracker *stageTracker) chan struct{} {
	t.Helper()
	signal := make(chan struct{}, 4)
	tracker.onHeldRefusal(func() { signal <- struct{}{} })
	return signal
}

func awaitSignal(t *testing.T, signal chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-t.Context().Done():
		t.Fatal("held refusal did not signal")
	}
}

func TestHeldStageRefusalSignalsOnceForAParkedProgram(t *testing.T) {
	tracker := &stageTracker{}
	ctx := withStageTracker(t.Context(), tracker)
	lease := &stageLease{}
	attachProgramStage(ctx, confinedStage(lease))
	signal := heldRefusalSignal(t, tracker)

	lease.refuse("/Users/person/.lima/colima/ha.sock")
	awaitSignal(t, signal)
	lease.refuse("/Users/person/.lima/colima/ssh.sock")
	later := &stageLease{}
	attachProgramStage(ctx, confinedStage(later))
	later.refuse("/dev/bpf0")
	if len(signal) != 0 {
		t.Fatalf("held refusals signalled %d more times", len(signal))
	}
}

func TestHeldStageRefusedBeforeTheParkSignals(t *testing.T) {
	tracker := &stageTracker{}
	ctx := withStageTracker(t.Context(), tracker)
	lease := &stageLease{}
	attachProgramStage(ctx, confinedStage(lease))
	lease.refuse("/Users/person/.lima/colima/ha.sock")
	awaitSignal(t, heldRefusalSignal(t, tracker))
}

func TestStageHeldAfterTheParkSignalsWhenRefused(t *testing.T) {
	tracker := &stageTracker{}
	ctx := withStageTracker(t.Context(), tracker)
	signal := heldRefusalSignal(t, tracker)
	lease := &stageLease{}
	lease.refuse("/Users/person/.lima/colima/ha.sock")
	attachProgramStage(ctx, confinedStage(lease))
	awaitSignal(t, signal)
}

func TestHeldRefusalsAfterContinuationBelongToTheJob(t *testing.T) {
	hooks, notices := refusalNotices()
	registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), hooks)
	tracker := &stageTracker{}
	ctx := withStageTracker(t.Context(), tracker)
	lease := &stageLease{}
	attachProgramStage(ctx, confinedStage(lease))
	continueAsJob(t, registry, tracker)
	signal := heldRefusalSignal(t, tracker)

	lease.refuse("/Users/person/.lima/colima/ha.sock")
	nextNotice(t, notices)
	if len(signal) != 0 {
		t.Fatal("a refusal on a job's stage signalled the park")
	}
}

func TestFloorRefusalOnAHeldStageDoesNotSignal(t *testing.T) {
	tracker := &stageTracker{}
	ctx := withStageTracker(t.Context(), tracker)
	signal := heldRefusalSignal(t, tracker)
	lease := &stageLease{}
	attachProgramStage(ctx, confinedStage(lease))
	lease.mu.Lock()
	lease.refusals = append(lease.refusals, confine.SandboxRefusal{Operation: "file-read-data", Target: "/proc/kcore", Count: 1, Recovery: confine.RecoverNone})
	listeners := append([]func(){}, lease.listeners...)
	lease.mu.Unlock()
	for _, listen := range listeners {
		listen()
	}
	tracker.mu.Lock()
	armed := tracker.refused != nil
	tracker.mu.Unlock()
	if !armed || len(signal) != 0 {
		t.Fatal("a floor refusal no capability opens signalled the park")
	}
}
