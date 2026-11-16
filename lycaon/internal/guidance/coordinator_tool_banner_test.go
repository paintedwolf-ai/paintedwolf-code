package guidance

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRenderWorkerInFlightRoster(t *testing.T) {
	setTestRenderer(t)
	ctx := context.Background()
	note, err := RenderWorkerInFlightRoster(ctx, BuildWorkerRosterLines([]api.WorkerTask{
		{ID: "8a3f12ab-cdef", AgentType: "implementer", Status: api.WorkerStatusPending, Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"internal/auth/**"}}},
		{ID: "9c12dead-beef", AgentType: "path-explorer", Status: api.WorkerStatusRunning},
	}))
	testutil.FailErr(t, "RenderWorkerInFlightRoster failed", err)
	for _, want := range []string{"8a3f", "implementer", "9c12", "path-explorer", "write · focus: internal/auth/**", "BANNER_WORKER_INFLIGHT_ROSTER"} {
		if !strings.Contains(note, want) {
			t.Fatalf("missing %q in note: %q", want, note)
		}
	}
}

func TestRosterJobIDPrefix(t *testing.T) {
	if got := RosterJobIDPrefix("8a3f12ab-cdef"); got != "8a3f…" {
		t.Fatalf("got %q", got)
	}
}
