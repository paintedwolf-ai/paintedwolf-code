package packboard_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFormatWorkerDetailLineIncludesTurns(t *testing.T) {
	now := time.Date(2026, 6, 16, 12, 2, 0, 0, time.UTC)
	started := now.Add(-2 * time.Minute)
	line := packboard.FormatWorkerDetailLine(api.WorkerTask{
		AgentType:     "implementer",
		Status:        api.WorkerStatusRunning,
		StartedAt:     &started,
		ToolLoopsUsed: 34,
	}, now)
	for _, want := range []string{"Task: implementer", "running", "2m", "34"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line = %q missing %q", line, want)
		}
	}
}

func TestFormatWorkerDetailLineOmitsTurnsBeforeFirst(t *testing.T) {
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	line := packboard.FormatWorkerDetailLine(api.WorkerTask{
		AgentType: "implementer",
		Status:    api.WorkerStatusRunning,
	}, now)
	if strings.Count(line, " · ") != 1 {
		t.Fatalf("line = %q should carry only its status segment", line)
	}
}

func TestFormatInFlightWorkerPulseLineIncludesGoalAndTurns(t *testing.T) {
	line := packboard.FormatInFlightWorkerPulseLine(api.WorkerTask{
		ID:            "job-abc12345",
		Brief:         "Implement models, migrations, handlers, and authentication",
		AgentType:     "repo-researcher",
		Status:        api.WorkerStatusRunning,
		ToolLoopsUsed: 34,
		Scope:         &api.TaskScope{Mode: api.TaskScopeModeRead, Paths: []string{"internal/**"}},
	})
	if !strings.Contains(line, "34") || !strings.Contains(line, "models, migrations, handlers, and authentication") {
		t.Fatalf("line = %q", line)
	}
}

func TestBuildInjectLinesPulseWorkerTurnsOnDetailLine(t *testing.T) {
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	started := now.Add(-3 * time.Minute)
	snap := api.BoardSnapshot{
		Workers: &api.BoardWorkersSlice{
			"tasks": []api.WorkerTask{
				{
					ID:            "job-turns",
					AgentType:     "implementer",
					Status:        api.WorkerStatusRunning,
					StartedAt:     &started,
					ToolLoopsUsed: 34,
				},
			},
		},
	}
	lines := packboard.BuildInjectLines(snap, packboard.InjectScopePulse, packboard.OrientOpts{Now: now})
	text := strings.Join(lines, "\n")
	if !strings.Contains(text, "34") {
		t.Fatalf("pulse inject = %q", text)
	}
}
