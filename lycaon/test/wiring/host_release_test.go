package wiring

import (
	"runtime"
	"testing"
	"time"
	"weak"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// A closed host must become garbage once a later host replaces the
// process-wide hooks that track only the latest one. Every E2E suite builds
// one host per test in a single process, so a host that stays reachable after
// Close accumulates hundreds of graphs and exhausts the runner's memory.
func TestClosedHostIsCollectable(t *testing.T) {
	var closed weak.Pointer[session.Host]
	t.Run("closed host", func(t *testing.T) {
		h := BuildForTest(t)
		_, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, h.ProjectDir(t, "project"))
		testutil.FailErr(t, "create session", err)
		closed = weak.Make(h.Sessions.Manager)
	})
	t.Run("replacing host", func(t *testing.T) {
		BuildForTest(t)
	})

	deadline := time.Now().Add(testutil.Timeout(10 * time.Second))
	for closed.Value() != nil {
		if time.Now().After(deadline) {
			t.Fatal("closed host is still reachable: a process-wide registry (observer list, hook, or cache) " +
				"still references it; return a release from the registration and track it in the host's resources")
		}
		runtime.GC()
		time.Sleep(50 * time.Millisecond)
	}
}
