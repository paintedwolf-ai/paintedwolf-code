package bgprocess

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

// recordingLease reports refusals the test records, as a kernel feed would.
type recordingLease struct {
	mu        sync.Mutex
	refusals  confine.SandboxRefusals
	listeners []func()
}

func (l *recordingLease) Refusals() confine.SandboxRefusals {
	l.mu.Lock()
	defer l.mu.Unlock()
	return confine.SandboxRefusals{Witness: confine.WitnessKernel, Refusals: append([]confine.SandboxRefusal(nil), l.refusals.Refusals...)}
}

func (l *recordingLease) OnRefusal(fn func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.listeners = append(l.listeners, fn)
}

func (l *recordingLease) refuse(operation, target string, recovery confine.RefusalRecovery) {
	l.mu.Lock()
	l.refusals.Refusals = append(l.refusals.Refusals, confine.SandboxRefusal{Operation: operation, Target: target, Count: 1, Recovery: recovery})
	listeners := append([]func(){}, l.listeners...)
	l.mu.Unlock()
	for _, listen := range listeners {
		listen()
	}
}

func appliedStage(lease StageLease) ProgramStage {
	return ProgramStage{Boundary: confine.Boundary{Applied: true}, Lease: lease}
}

// noticeRecorder collects refusal notices in arrival order.
type noticeRecorder struct{ notices chan RefusalNotice }

func newNoticeRecorder() *noticeRecorder {
	return &noticeRecorder{notices: make(chan RefusalNotice, 8)}
}

func (n *noticeRecorder) publish(_ context.Context, notice RefusalNotice) { n.notices <- notice }

func (n *noticeRecorder) next(t *testing.T) RefusalNotice {
	t.Helper()
	select {
	case notice := <-n.notices:
		return notice
	case <-t.Context().Done():
		t.Fatal("no refusal notice arrived")
		return RefusalNotice{}
	}
}

func (n *noticeRecorder) none(t *testing.T) {
	t.Helper()
	select {
	case notice := <-n.notices:
		t.Fatalf("unexpected refusal notice %+v", notice)
	default:
	}
}

// runningProgram starts a program job that runs until the test releases it.
func runningProgram(t *testing.T, r *Registry, history ProgramHistory) (handle string, finish func()) {
	t.Helper()
	release := make(chan struct{})
	handle, err := r.StartProgram(t.Context(), ProgramJobSpec{
		SessionID: "session", CommandLine: "colima start", OriginTool: "command", ToolCallID: "call", History: history,
		Run: func(ctx context.Context, _, _ io.Writer) (ProgramJobResult, error) {
			select {
			case <-release:
				return ProgramJobResult{}, nil
			case <-ctx.Done():
				return ProgramJobResult{ExitCode: 130}, ctx.Err()
			}
		},
	})
	testutil.FailErr(t, "start program job", err)
	var once sync.Once
	finish = func() { once.Do(func() { close(release) }) }
	t.Cleanup(finish)
	return handle, finish
}

func TestContinuedProgramStartsFromItsForegroundHistory(t *testing.T) {
	r := NewRegistry(DefaultConfig(), Hooks{})
	shown := confine.SandboxRefusals{Witness: confine.WitnessKernel, Refusals: []confine.SandboxRefusal{
		{Operation: "file-write-create", Target: "/Users/person/.lima/colima/lima.yaml", Count: 1, Recovery: confine.RecoverWriteRoot},
	}}
	handle, _ := runningProgram(t, r, ProgramHistory{
		Boundary: confine.Boundary{Applied: true}, Settled: shown,
		Network: []confine.EgressHost{{Host: "github.com", Port: 443}},
	})

	snapshot, err := r.Output.ReadRawOutput("session", handle, 0)
	testutil.FailErr(t, "read program output", err)
	if !snapshot.Confinement.Boundary.Applied {
		t.Fatal("a continued confined program reported no applied boundary")
	}
	if got := snapshot.Confinement.Refusals().Refusals; len(got) != 1 || got[0].Target != shown.Refusals[0].Target {
		t.Fatalf("continued program refusals = %+v", got)
	}
	if got := snapshot.Confinement.Network(); len(got) != 1 || got[0].Host != "github.com" {
		t.Fatalf("continued program network = %+v", got)
	}
	if unshown := r.Output.UnshownRefusals("session", handle); unshown != 0 {
		t.Fatalf("refusals the promoting result showed counted as unshown: %d", unshown)
	}
}

func TestAttachedStageReportsRefusalsFromBeforeAndAfterItAttached(t *testing.T) {
	notices := newNoticeRecorder()
	r := NewRegistry(DefaultConfig(), Hooks{Refused: notices.publish})
	handle, _ := runningProgram(t, r, ProgramHistory{})
	lease := &recordingLease{}
	lease.refuse("network-bind", "/Users/person/.lima/colima/ha.sock", confine.RecoverHostExecution)

	r.AttachProgramStage(t.Context(), "session", handle, appliedStage(lease))
	first := notices.next(t)
	if first.Unshown != 1 || len(first.Observation.Refusals.Refusals) != 1 || first.Observation.Refusals.Refusals[0].Recovery != confine.RecoverHostExecution {
		t.Fatalf("refusal recorded before attach = %+v", first)
	}

	lease.refuse("network-bind", "/Users/person/.lima/colima/ssh.sock", confine.RecoverHostExecution)
	second := notices.next(t)
	if second.Unshown != 1 || len(second.Observation.Refusals.Refusals) != 2 {
		t.Fatalf("refusal recorded after attach = %+v", second)
	}
	notices.none(t)
}

func TestStageLaunchedAfterContinuationCountsTowardTheJob(t *testing.T) {
	notices := newNoticeRecorder()
	r := NewRegistry(DefaultConfig(), Hooks{Refused: notices.publish})
	handle, _ := runningProgram(t, r, ProgramHistory{Boundary: confine.Boundary{Applied: true}})
	first, second := &recordingLease{}, &recordingLease{}
	r.AttachProgramStage(t.Context(), "session", handle, appliedStage(first))
	settleSecond := r.AttachProgramStage(t.Context(), "session", handle, appliedStage(second))

	second.refuse("file-write-create", "/opt/shared/out", confine.RecoverWriteRoot)
	if notice := notices.next(t); notice.Unshown != 1 {
		t.Fatalf("second stage notice = %+v", notice)
	}
	settleSecond(second.Refusals(), nil)
	snapshot, err := r.Output.ReadRawOutput("session", handle, 0)
	testutil.FailErr(t, "read program output", err)
	if got := snapshot.Confinement.Refusals().Refusals; len(got) != 1 || got[0].Recovery != confine.RecoverWriteRoot {
		t.Fatalf("settled second stage refusals = %+v", got)
	}
}

func TestQuietStagesScheduleNoRefusalNotice(t *testing.T) {
	notices := newNoticeRecorder()
	r := NewRegistry(DefaultConfig(), Hooks{Refused: notices.publish})
	handle, _ := runningProgram(t, r, ProgramHistory{Boundary: confine.Boundary{Applied: true}})
	settle := r.AttachProgramStage(t.Context(), "session", handle, appliedStage(&recordingLease{}))
	settle(confine.SandboxRefusals{}, nil)
	proc, err := r.jobs.lookup("session", handle)
	testutil.FailErr(t, "look up program job", err)
	r.jobs.mu.Lock()
	scheduled := proc.refusalNotice != nil
	r.jobs.mu.Unlock()
	if scheduled {
		t.Fatal("a quiet stage scheduled a refusal notice")
	}
}

func TestStoppedJobCompletionCountsRefusalsNothingShowed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		refuse  bool
		unshown int
	}{
		{"refused", true, 1},
		{"quiet", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			completions := make(chan Completion, 1)
			r := NewRegistry(DefaultConfig(), Hooks{Complete: func(_ context.Context, completion Completion) { completions <- completion }})
			handle, _ := runningProgram(t, r, ProgramHistory{Boundary: confine.Boundary{Applied: true}})
			lease := &recordingLease{}
			settle := r.AttachProgramStage(t.Context(), "session", handle, appliedStage(lease))
			if tc.refuse {
				lease.refuse("network-bind", "/Users/person/.lima/colima/ha.sock", confine.RecoverHostExecution)
			}
			if got := r.Output.UnshownRefusals("session", handle); got != tc.unshown {
				t.Fatalf("unshown before stop = %d, want %d", got, tc.unshown)
			}
			settle(lease.Refusals(), nil)
			_, err := r.Lifecycle.Stop("session", handle, TerminationStoppedByAgent)
			testutil.FailErr(t, "stop program job", err)
			completion := <-completions
			if completion.TerminationReason != TerminationStoppedByAgent || completion.UnshownRefusals != tc.unshown {
				t.Fatalf("completion termination=%s unshown=%d, want stopped_by_agent/%d", completion.TerminationReason, completion.UnshownRefusals, tc.unshown)
			}
		})
	}
}
