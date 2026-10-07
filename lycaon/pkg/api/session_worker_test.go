package api

import "testing"

func TestSessionIsWorkerChild(t *testing.T) {
	if (&Session{ParentSessionID: "parent-1"}).IsWorkerChild() != true {
		t.Fatal("child session")
	}
	if (&Session{ID: "coord-1"}).IsWorkerChild() {
		t.Fatal("coordinator session")
	}
	var nilSess *Session
	if nilSess.IsWorkerChild() {
		t.Fatal("nil session")
	}
}

func TestWorkerTaskLegStatusUsesHostGrade(t *testing.T) {
	cases := []struct {
		name string
		task WorkerTask
		want string
	}{
		{
			name: "running non-terminal",
			task: WorkerTask{Status: WorkerStatusRunning},
			want: "running",
		},
		{
			name: "failed terminal",
			task: WorkerTask{Status: WorkerStatusFailed},
			want: "failed",
		},
		{
			name: "canceled terminal",
			task: WorkerTask{Status: WorkerStatusCanceled},
			want: "canceled",
		},
		{
			name: "complete no result",
			task: WorkerTask{Status: WorkerStatusComplete},
			want: "unreported",
		},
		{
			name: "complete nil report",
			task: WorkerTask{Status: WorkerStatusComplete, Result: &WorkerResult{}},
			want: "unreported",
		},
		{
			name: "complete host grade partial",
			task: WorkerTask{
				Status: WorkerStatusComplete,
				Result: &WorkerResult{
					CompletionReport: &WorkerCompletionReport{LegStatus: "partial"},
				},
			},
			want: "partial",
		},
		{
			name: "complete host grade complete",
			task: WorkerTask{
				Status: WorkerStatusComplete,
				Result: &WorkerResult{
					CompletionReport: &WorkerCompletionReport{LegStatus: "complete"},
				},
			},
			want: "complete",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := WorkerTaskLegStatus(tc.task)
			if got != tc.want {
				t.Fatalf("WorkerTaskLegStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReviewRequiresHostGradedComplete(t *testing.T) {
	taskComplete := WorkerTask{
		Status: WorkerStatusComplete,
		Result: &WorkerResult{
			CompletionReport: &WorkerCompletionReport{LegStatus: "complete"},
		},
	}
	if !WorkerReviewSucceeded(taskComplete) {
		t.Fatal("expected WorkerReviewSucceeded to be true for complete leg")
	}

	taskPartial := WorkerTask{
		Status: WorkerStatusComplete,
		Result: &WorkerResult{
			CompletionReport: &WorkerCompletionReport{LegStatus: "partial"},
		},
	}
	if WorkerReviewSucceeded(taskPartial) {
		t.Fatal("expected WorkerReviewSucceeded to be false for partial leg")
	}

	taskFailed := WorkerTask{
		Status: WorkerStatusFailed,
		Result: &WorkerResult{
			CompletionReport: &WorkerCompletionReport{LegStatus: "complete"},
		},
	}
	if WorkerReviewSucceeded(taskFailed) {
		t.Fatal("expected WorkerReviewSucceeded to be false for failed task")
	}
}
