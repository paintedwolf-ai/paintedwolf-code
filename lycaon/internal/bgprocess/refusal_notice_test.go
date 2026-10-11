package bgprocess

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
)

func TestRefusalNoticePublishesOnlyUnshownRefusals(t *testing.T) {
	var notices []RefusalNotice
	r := NewRegistry(DefaultConfig(), Hooks{Refused: func(_ context.Context, notice RefusalNotice) {
		notices = append(notices, notice)
	}})
	proc := &Process{Handle: "job", SessionID: "session", running: true, mode: JobModeBackground, originTool: "command", boundary: confine.Boundary{Applied: true}}
	r.jobs.sessions[proc.SessionID] = map[string]*Process{proc.Handle: proc}
	refusals := confine.SandboxRefusals{Witness: confine.WitnessKernel, Refusals: []confine.SandboxRefusal{
		{Operation: "network-bind", Target: "/tmp/first.sock", Count: 1, Recovery: confine.RecoverHostExecution},
	}}
	r.Output.publishRefusalSnapshot(t.Context(), proc, refusals)
	r.Output.publishRefusalSnapshot(t.Context(), proc, refusals)
	if len(notices) != 1 || notices[0].Unshown != 1 || notices[0].Handle != proc.Handle || !notices[0].Observation.Running {
		t.Fatalf("first refusal notices = %+v", notices)
	}
	refusals.Refusals = append(refusals.Refusals, confine.SandboxRefusal{Operation: "signal", Count: 1, Recovery: confine.RecoverProcessControl})
	r.Output.NoteRefusalsShown(proc.SessionID, proc.Handle, 2)
	r.Output.NoteRefusalsShown(proc.SessionID, proc.Handle, 1)
	r.Output.publishRefusalSnapshot(t.Context(), proc, refusals)
	if len(notices) != 1 {
		t.Fatalf("already shown refusal produced %d notices", len(notices))
	}
	refusals.Refusals = append(refusals.Refusals, confine.SandboxRefusal{Operation: "network-bind", Target: "/tmp/next.sock", Count: 1, Recovery: confine.RecoverHostExecution})
	r.Output.publishRefusalSnapshot(t.Context(), proc, refusals)
	if len(notices) != 2 || notices[1].Unshown != 1 || len(notices[1].Observation.Refusals.Refusals) != 3 {
		t.Fatalf("later refusal notices = %+v", notices)
	}
}

func TestRefusalNoticeStaysQuietForInvisibleOrEndedJobs(t *testing.T) {
	for _, tc := range []struct {
		name string
		proc *Process
	}{
		{"awaited", &Process{running: true, silent: true, mode: JobModeAwaited}},
		{"silent background", &Process{running: true, silent: true, mode: JobModeBackground}},
		{"discarded", &Process{running: true, discarded: true}},
		{"ended", &Process{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry(DefaultConfig(), Hooks{Refused: func(context.Context, RefusalNotice) {
				t.Fatal("invisible or ended job published a refusal notice")
			}})
			r.Output.publishRefusalSnapshot(t.Context(), tc.proc, confine.SandboxRefusals{Refusals: []confine.SandboxRefusal{{Operation: "signal", Count: 1}}})
			if tc.proc.refusalsShown != 0 {
				t.Fatal("suppressed notice consumed an unshown refusal")
			}
		})
	}
}

func TestRefusalNoticeCoalescesOneBurst(t *testing.T) {
	r := NewRegistry(DefaultConfig(), Hooks{Refused: func(context.Context, RefusalNotice) {}})
	proc := &Process{running: true}
	r.Output.refusalObserved(t.Context(), proc)
	r.jobs.mu.Lock()
	first := proc.refusalNotice
	r.jobs.mu.Unlock()
	if first == nil {
		t.Fatal("running job did not schedule a refusal notice")
	}
	defer first.Stop()
	r.Output.refusalObserved(t.Context(), proc)
	r.jobs.mu.Lock()
	same := proc.refusalNotice == first
	proc.running = false
	r.jobs.mu.Unlock()
	if !same {
		t.Fatal("a second refusal scheduled another notice in the same burst")
	}
}

func TestRefusalNoticeRetainsDetachedProcessContext(t *testing.T) {
	type contextKey struct{}
	request, cancel := context.WithCancel(context.WithValue(t.Context(), contextKey{}, "process-owner"))
	process := context.WithoutCancel(request)
	cancel()
	var published context.Context
	r := NewRegistry(DefaultConfig(), Hooks{Refused: func(ctx context.Context, _ RefusalNotice) {
		published = ctx
	}})
	proc := &Process{running: true}
	r.Output.publishRefusalSnapshot(process, proc, confine.SandboxRefusals{Refusals: []confine.SandboxRefusal{{Operation: "signal", Count: 1}}})
	if published == nil || published.Value(contextKey{}) != "process-owner" || published.Err() != nil {
		t.Fatalf("refusal notice lost its process context: %v", published)
	}
}
