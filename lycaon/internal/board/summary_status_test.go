package board

import (
	"fmt"
	"reflect"
	"slices"
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

// Delivery, lifecycle, and overlay integration are independent axes. A stale
// report or a clean merge must never turn an unfinished run into delivered work.
func TestWorkerOutcomeConservationAcrossStateProduct(t *testing.T) {
	statuses := append(api.AllWorkerStatuses(), api.WorkerStatus("future-state"))
	grades := []string{"complete", "partial", "blocked", "", "future-grade"}
	merges := []api.WorkerMergeStatus{"", api.WorkerMergeStatusPending, api.WorkerMergeStatusMerged, api.WorkerMergeStatusRejected}
	var corpus []api.WorkerTask
	expected := packboard.WorkerOutcomeCounts{}
	terminalCount := 0
	for _, status := range statuses {
		for _, grade := range grades {
			for _, merge := range merges {
				task := api.WorkerTask{ID: fmt.Sprintf("%s/%s/%s", status, grade, merge), Status: status, MergeStatus: merge,
					Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: grade}}}
				corpus = append(corpus, task)
				want := ""
				switch status {
				case api.WorkerStatusFailed, api.WorkerStatusCanceled:
					want = string(status)
				case api.WorkerStatusComplete:
					want = grade
					if grade == "" || grade == "future-grade" {
						want = "unreported"
					}
				case api.WorkerStatusPending, api.WorkerStatusRunning, api.WorkerStatusWaiting, api.WorkerStatusHeld:
				default:
				}
				t.Run(task.ID, func(t *testing.T) {
					counts := packboard.CountWorkerOutcomes([]api.WorkerTask{task})
					if want == "" {
						if counts.Total() != 0 {
							t.Fatalf("unfinished task counted as returned: %v", counts)
						}
					} else if counts.Total() != 1 || counts[want] != 1 {
						t.Fatalf("outcome=%v want exactly one %s", counts, want)
					}
					snap := api.BoardSnapshot{Workers: &api.BoardWorkersSlice{"tasks": []api.WorkerTask{task}}}
					summary := BuildBoardSummary(snap)
					if strings.Contains(summary, "All workers complete") != (status == api.WorkerStatusComplete && grade == "complete") {
						t.Fatalf("delivery claim changed with unrelated state: %s", summary)
					}
				})
				if want != "" {
					expected[want]++
					terminalCount++
				}
			}
		}
	}
	now := time.Unix(1, 0)
	headline := func(tasks []api.WorkerTask) string {
		return BuildBoardSummary(api.BoardSnapshot{Workers: &api.BoardWorkersSlice{"tasks": tasks}})
	}
	before := headline(corpus)
	for pass := 0; pass < 2; pass++ {
		counts := packboard.CountWorkerOutcomes(corpus)
		if !reflect.DeepEqual(counts, expected) || counts.Total() != terminalCount {
			t.Fatalf("returned tasks lost or duplicated: %v want %v", counts, expected)
		}
		if headline(corpus) != before {
			t.Fatal("board outcome depends on worker ordering")
		}
		line := packboard.FormatWorkLine(corpus, now)
		for grade, count := range expected {
			label := fmt.Sprintf("%d %s", count, grade)
			if !strings.Contains(before, label) || !strings.Contains(line, label) {
				t.Fatalf("board projections disagree on %s: summary=%s line=%s", label, before, line)
			}
		}
		slices.Reverse(corpus)
	}
}
