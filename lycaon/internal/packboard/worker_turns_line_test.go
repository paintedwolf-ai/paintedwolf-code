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

