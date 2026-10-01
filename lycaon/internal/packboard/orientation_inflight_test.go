package packboard_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFormatWorkLineInFlightRoster(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	line := packboard.FormatWorkLine([]api.WorkerTask{
		{ID: "8a3f12ab", AgentType: "implementer", Status: api.WorkerStatusPending},
		{ID: "9c12dead", AgentType: "path-explorer", Status: api.WorkerStatusRunning},
	}, now)
	if !strings.Contains(line, "Work: 2 in flight") {
		t.Fatalf("line = %q", line)
	}
	if !strings.Contains(line, "8a3f") || !strings.Contains(line, "implementer") {
		t.Fatalf("line = %q", line)
	}
	if !strings.Contains(line, "9c12") || !strings.Contains(line, "path-explorer") {
		t.Fatalf("line = %q", line)
	}
}

func TestBuildInjectLinesPulseIncludesInFlightDetail(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	snap := api.BoardSnapshot{
		Workers: &api.BoardWorkersSlice{
			"tasks": []api.WorkerTask{
				{ID: "job-abc12345", AgentType: "repo-researcher", Status: api.WorkerStatusRunning, Scope: &api.TaskScope{Mode: api.TaskScopeModeRead, Paths: []string{"internal/**"}}},
			},
		},
	}
	lines := packboard.BuildInjectLines(snap, packboard.InjectScopePulse, packboard.OrientOpts{Now: now})
	text := strings.Join(lines, "\n")
	if !strings.Contains(text, "Work: 1 in flight") {
		t.Fatalf("lines = %q", text)
	}
	if !strings.Contains(text, "In flight: job-") || !strings.Contains(text, "repo-researcher") {
		t.Fatalf("lines = %q", text)
	}
	if !strings.Contains(text, "read · focus: internal/**") {
		t.Fatalf("lines = %q", text)
	}
}
