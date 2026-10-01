package survey

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestShouldCurateSynthesisBoundaries(t *testing.T) {
	cases := []struct {
		name  string
		stats SnapshotStats
		want  bool
	}{
		{"under min single worker", SnapshotStats{MergedBytes: 8000, WorkerCount: 1}, false},
		{"at min single worker", SnapshotStats{MergedBytes: 8001, WorkerCount: 1}, false},
		{"two workers under min", SnapshotStats{MergedBytes: 8000, WorkerCount: 2}, false},
		{"two workers over min", SnapshotStats{MergedBytes: 8001, WorkerCount: 2}, true},
		{"single worker over budget", SnapshotStats{MergedBytes: 24001, WorkerCount: 1}, true},
		{"at budget single worker", SnapshotStats{MergedBytes: 24000, WorkerCount: 1}, false},
		{"zero workers", SnapshotStats{MergedBytes: 50000, WorkerCount: 0}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldCurateSynthesis(tc.stats); got != tc.want {
				t.Fatalf("ShouldCurateSynthesis(%+v) = %v want %v", tc.stats, got, tc.want)
			}
		})
	}
}

func TestBuildSynthesisSnapshotWorkerBody(t *testing.T) {
	snapshot, stats, err := BuildSynthesisSnapshot(context.Background(), SnapshotInput{
		Envelopes: []WorkerEnvelope{{
			JobID:     "j1",
			AgentType: "scout",
			Body:      "worker proof",
			Report:    WorkerReportSnapshot{LegStatus: "complete"},
		}},
		MergedBytes: 100,
		WorkerCount: 1,
	})
	testutil.FailErr(t, "BuildSynthesisSnapshot failed", err)
	if stats.RecordCount == 0 {
		t.Fatal("expected report body record in snapshot")
	}
	if len(snapshot.Handles) == 0 {
		t.Fatal("expected snapshot handles")
	}
}
