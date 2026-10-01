package packboard_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFormatWorkerDetailLineIncludesBudget(t *testing.T) {
	now := time.Date(2026, 6, 16, 12, 2, 0, 0, time.UTC)
	started := now.Add(-2 * time.Minute)
	line := packboard.FormatWorkerDetailLine(api.WorkerTask{
		AgentType:     "implementer",
		Status:        api.WorkerStatusRunning,
		StartedAt:     &started,
		MaxToolLoops:  40,
		ToolLoopsUsed: 34,
	}, now)
	for _, want := range []string{"Task: implementer", "running", "2m", "34/40"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line = %q missing %q", line, want)
		}
	}
}

func TestFormatWorkerDetailLineOmitsBudgetWithoutMax(t *testing.T) {
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	line := packboard.FormatWorkerDetailLine(api.WorkerTask{
		AgentType: "implementer",
		Status:    api.WorkerStatusRunning,
	}, now)
	if strings.Contains(line, "/") {
		t.Fatalf("line = %q should omit budget segment", line)
	}
}

func TestFormatInFlightWorkerPulseLineIncludesGoalAndBudget(t *testing.T) {
	line := packboard.FormatInFlightWorkerPulseLine(api.WorkerTask{
		ID:            "job-abc12345",
		Brief:         "Implement models, migrations, handlers, and authentication",
		AgentType:     "repo-researcher",
		Status:        api.WorkerStatusRunning,
		MaxToolLoops:  40,
		ToolLoopsUsed: 34,
		Scope:         &api.TaskScope{Mode: api.TaskScopeModeRead, Paths: []string{"internal/**"}},
	})
	if !strings.Contains(line, "34/40") || !strings.Contains(line, "models, migrations, handlers, and authentication") {
		t.Fatalf("line = %q", line)
	}
}

func TestBuildInjectLinesPulseWorkerBudgetOnDetailLine(t *testing.T) {
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	started := now.Add(-3 * time.Minute)
	snap := api.BoardSnapshot{
		Workers: &api.BoardWorkersSlice{
			"tasks": []api.WorkerTask{
				{
					ID:            "job-budget",
					AgentType:     "implementer",
					Status:        api.WorkerStatusRunning,
					StartedAt:     &started,
					MaxToolLoops:  40,
					ToolLoopsUsed: 34,
				},
			},
		},
	}
	lines := packboard.BuildInjectLines(snap, packboard.InjectScopePulse, packboard.OrientOpts{Now: now})
	text := strings.Join(lines, "\n")
	if !strings.Contains(text, "34/40") {
		t.Fatalf("pulse inject = %q", text)
	}
}
