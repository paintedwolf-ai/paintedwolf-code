package board

import (
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerSummariesPreserveEveryUnfinishedStatus(t *testing.T) {
	complete := &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}
	statuses := append(api.AllWorkerStatuses(), api.WorkerStatus("unknown"))
	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			tasks := []api.WorkerTask{
				{ID: "finished", Status: api.WorkerStatusComplete, Result: complete},
				{ID: "remaining", AgentType: "reviewer", Status: status, Result: complete},
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
			} else if !strings.Contains(line, "complete") {
				t.Fatalf("terminal work not counted: %q", line)
			}
			if (status == api.WorkerStatusFailed || status == api.WorkerStatusCanceled) && !strings.Contains(summary, "1 "+string(status)) {
				t.Fatalf("unsuccessful worker omitted from mixed result: %q", summary)
			}
		})
	}
}

func TestWorkerSummariesPreserveGradedDelivery(t *testing.T) {
	for _, grade := range []string{"complete", "partial", "blocked", "", "unknown"} {
		t.Run(grade, func(t *testing.T) {
			result := &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: grade}}
			if grade == "" {
				result = nil
			}
			task := api.WorkerTask{ID: "returned", Status: api.WorkerStatusComplete, Result: result}
			want := grade
			if want == "" || want == "unknown" {
				want = "unreported"
			}
			for _, active := range []bool{false, true} {
				tasks := []api.WorkerTask{task}
				if active {
					tasks = append(tasks, api.WorkerTask{ID: "active", Status: api.WorkerStatusRunning})
				}
				snap := api.BoardSnapshot{Workers: &api.BoardWorkersSlice{"tasks": tasks}}
				lines := packboard.BuildInjectLines(snap, packboard.InjectScopePulse, packboard.OrientOpts{Now: time.Now()})
				for _, text := range []string{BuildBoardSummary(snap), strings.Join(lines, "\n")} {
					if !strings.Contains(text, "1 "+want) {
						t.Fatalf("grade %q, active %v lost in %q", grade, active, text)
					}
					if grade != "complete" && strings.Contains(text, "All workers complete") {
						t.Fatalf("unfinished delivery called complete: %q", text)
					}
				}
			}
		})
	}
}
