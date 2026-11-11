package command_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProgramActivitiesPublishStartsBeforeCallCompletion(t *testing.T) {
	f := newProgramFixture(t)
	var mu sync.Mutex
	var snapshots []*api.CommandProgramReceipt
	f.Configure = func(tc *tools.ToolContext) {
		tc.Effects.ReportProgress = func(progress api.ToolProgress) {
			if progress.CommandProgram != nil {
				mu.Lock()
				snapshots = append(snapshots, progress.CommandProgram)
				mu.Unlock()
			}
		}
	}
	output, err := f.call(t, t.Context(), "external -- /bin/echo first | head; external -- /bin/echo second")
	testutil.FailErr(t, "run actual observed pipeline and following process", err)
	if !strings.Contains(output, "second") {
		t.Fatal("observer changed process output")
	}
	mu.Lock()
	defer mu.Unlock()
	sawRunning := false
	var revision int64
	presented := map[string]bool{}
	for _, snapshot := range snapshots {
		if snapshot.ActivityRevision < revision {
			t.Fatal("live activity revision moved backwards")
		}
		revision = snapshot.ActivityRevision
		current := map[string]bool{}
		for _, activity := range snapshot.Activities {
			current[activity.ID] = true
			if activity.Label == "Run process" && activity.State == "running" {
				sawRunning = true
			}
		}
		for id := range presented {
			if !current[id] {
				t.Fatalf("visible activity %s disappeared", id)
			}
		}
		presented = current
	}
	if !sawRunning || len(presented) < 2 {
		t.Fatalf("missing immediate process activities: %+v", snapshots)
	}
}
