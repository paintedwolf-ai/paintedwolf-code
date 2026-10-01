package board_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerPulseRunningCount(t *testing.T) {
	now := time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC)
	tasks := []api.WorkerTask{
		{ID: "a", Status: api.WorkerStatusRunning, StartedAt: ptrTime(now.Add(-5 * time.Minute))},
		{ID: "b", Status: api.WorkerStatusRunning, StartedAt: ptrTime(now.Add(-2 * time.Minute))},
	}
	got := packboard.FormatWorkLine(tasks, now)
	if !strings.Contains(got, "2 in flight") {
		t.Fatalf("got %q", got)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
