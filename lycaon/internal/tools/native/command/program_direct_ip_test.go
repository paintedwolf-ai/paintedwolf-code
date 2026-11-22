package command_test

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// grantingDirectIP holds an authorized direct-IP permit for every action, so the
// launch runs without an approval card.
type grantingDirectIP struct{}

func (grantingDirectIP) IssuePermit(hitl.EffectPermitIdentity, string, string, string) {}
func (grantingDirectIP) ConsumePermit(hitl.EffectPermitIdentity, string, string, string) (bool, error) {
	return true, nil
}
func (grantingDirectIP) Authorized(hitl.EffectPermitIdentity, string) bool { return true }
func (grantingDirectIP) LeaseCovers(string, hitl.DirectIPLease) bool       { return true }
func (grantingDirectIP) GrantChat(string, hitl.DirectIPLease, string, string, *time.Time) bool {
	return true
}

// directIPPhases records the started and completed phases a program's
// direct-IP launches report; completed is closed by the first completion.
type directIPPhases struct {
	mu        sync.Mutex
	phases    []tools.DirectIPLifecyclePhase
	once      sync.Once
	completed chan struct{}
}

func (p *directIPPhases) record(event tools.DirectIPLifecycleEvent) {
	if event.Phase != tools.DirectIPLifecycleStarted && event.Phase != tools.DirectIPLifecycleCompleted {
		return
	}
	p.mu.Lock()
	p.phases = append(p.phases, event.Phase)
	p.mu.Unlock()
	if event.Phase == tools.DirectIPLifecycleCompleted {
		p.once.Do(func() { close(p.completed) })
	}
}

func (p *directIPPhases) seen() []tools.DirectIPLifecyclePhase {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.phases)
}

func newDirectIPFixture(t *testing.T) (*programFixture, *directIPPhases) {
	t.Helper()
	phases := &directIPPhases{completed: make(chan struct{})}
	f := &programFixture{programtest.New(t, programtest.Options{})}
	// The fixture denies all egress; a direct-IP launch needs the request to
	// decide it and the host's egress broker to bind the action.
	t.Setenv("LYCAON_SANDBOX_NETWORK", "")
	_, err := confine.StartEgressBroker(t.TempDir())
	testutil.FailErr(t, "start egress broker", err)
	t.Cleanup(func() { _ = confine.StopEgressBroker() })
	f.Runtime.Executor.Capabilities.SetDirectIPCapabilityRuntime(grantingDirectIP{})
	f.Runtime.Executor.Capabilities.SetDirectIPLifecycleHook(phases.record)
	return f, phases
}

// directIPCall asks for direct network access, as the model's command call does.
func directIPCall(source string) map[string]any {
	return map[string]any{"command": source, "capability_request": map[string]any{"direct_ip": true}}
}

// A program's external launch under direct IP reports started once the process
// runs and completed once it has been reaped.
func TestProgramExternalLaunchReportsDirectIPLifecycle(t *testing.T) {
	f, phases := newDirectIPFixture(t)
	_, err := f.CallArgs(t, t.Context(), directIPCall("external -- /bin/echo direct"))
	testutil.FailErr(t, "run direct-IP program", err)
	want := []tools.DirectIPLifecyclePhase{tools.DirectIPLifecycleStarted, tools.DirectIPLifecycleCompleted}
	if got := phases.seen(); !slices.Equal(got, want) {
		t.Fatalf("direct-IP lifecycle = %v, want %v", got, want)
	}
}

// A launch handed back as a running job reports completed only once the job's
// process is reaped.
func TestProgramExternalJobReportsDirectIPCompletionWhenReaped(t *testing.T) {
	f, phases := newDirectIPFixture(t)
	ready, stop := filepath.Join(f.Dir, "ready"), filepath.Join(f.Dir, "stop")
	_, err := f.CallArgs(t, t.Context(), directIPCall(`external -- /bin/sh -c ': > ready; while [ ! -f stop ]; do sleep 0.01; done' &`))
	testutil.FailErr(t, "launch direct-IP job", err)
	awaitPath(t, ready)
	if got := phases.seen(); !slices.Equal(got, []tools.DirectIPLifecyclePhase{tools.DirectIPLifecycleStarted}) {
		t.Fatalf("running job lifecycle = %v, want started only", got)
	}
	testutil.FailErr(t, "release job", os.WriteFile(stop, nil, 0o600))
	select {
	case <-phases.completed:
	case <-t.Context().Done():
		t.Fatal("the reaped job never reported completed")
	}
	want := []tools.DirectIPLifecyclePhase{tools.DirectIPLifecycleStarted, tools.DirectIPLifecycleCompleted}
	if got := phases.seen(); !slices.Equal(got, want) {
		t.Fatalf("reaped job lifecycle = %v, want %v", got, want)
	}
}

// awaitPath waits for a file the launched process creates once it runs.
func awaitPath(t *testing.T, path string) {
	t.Helper()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-t.Context().Done():
			t.Fatalf("%s never appeared", path)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
