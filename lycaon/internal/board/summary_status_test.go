package board

import (
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerSummariesPreserveEveryUnfinishedStatus(t *testing.T) {
	statuses := append(api.AllWorkerStatuses(), api.WorkerStatus("unknown"))
	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			tasks := []api.WorkerTask{
				{ID: "finished", Status: api.WorkerStatusComplete},
				{ID: "remaining", AgentType: "reviewer", Status: status},
			}
			snapshot := api.BoardSnapshot{Workers: &api.BoardWorkersSlice{"tasks": tasks}}
			summary := BuildBoardSummary(snapshot)
			line := packboard.FormatWorkLine(tasks, time.Now())
			terminal := status == api.WorkerStatusComplete || status == api.WorkerStatusFailed || status == api.WorkerStatusCanceled
			if strings.Contains(summary, "All workers complete") != (status == api.WorkerStatusComplete) {
				t.Fatalf("status %q misrepresented by summary %q", status, summary)
			}
			if !terminal {
				if !strings.Contains(summary, "1 worker(s) in flight") || !strings.Contains(line, "1 in flight") || !strings.Contains(line, "reviewer") {
					t.Fatalf("unfinished worker disappeared: summary=%q, line=%q", summary, line)
				}
			} else if !strings.Contains(line, "2 done") {
				t.Fatalf("terminal work not counted: %q", line)
			}
			if (status == api.WorkerStatusFailed || status == api.WorkerStatusCanceled) && !strings.Contains(summary, "1 failed") {
				t.Fatalf("unsuccessful worker omitted from mixed result: %q", summary)
			}
		})
	}
}
