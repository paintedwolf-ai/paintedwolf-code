package workercompletion

import (
	"errors"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestCitedResultsAreTheWorkersEarlierResults(t *testing.T) {
	earlier := []string{"res-1", "res-2"}
	if err := CheckCitedResults([]string{"res-2"}, earlier); err != nil {
		t.Fatalf("own earlier result refused: %v", err)
	}
	for _, tc := range []struct {
		name        string
		earlier     []string
		wantUnknown []string
	}{
		{"later round", earlier, []string{"res-9"}},
		{"first round", nil, []string{"res-1", "res-9"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckCitedResults([]string{"res-1", "res-9"}, tc.earlier)
			var reject *toolrejection.ToolReject
			if !errors.As(err, &reject) || reject.Code != ResultNotEarlierCode {
				t.Fatalf("err = %v", err)
			}
			if got := reject.Data["unknown_cited_results"].([]string); !slices.Equal(got, tc.wantUnknown) {
				t.Fatalf("unknown = %v, want %v", got, tc.wantUnknown)
			}
			// The fix line branches on this list, so it is present even when empty.
			if got := reject.Data["earlier_results"].([]string); got == nil || len(got) != len(tc.earlier) {
				t.Fatalf("earlier = %#v", got)
			}
		})
	}
}

func TestCitedResultsSurviveTheWire(t *testing.T) {
	report := WorkerCompletionReport{LegStatus: "complete", CitedResults: []string{"res-1"}}
	if got := ReportFromWire(ReportWire(report)).CitedResults; !slices.Equal(got, []string{"res-1"}) {
		t.Fatalf("cited results after wire = %v", got)
	}
	decoded, err := DecodeCompleteLegArgs(map[string]any{"leg_status": "complete", "brief": "done", "cited_results": []any{" res-1 ", "res-1"}})
	testutil.FailErr(t, "decode complete_leg args", err)
	if !slices.Equal(decoded.CitedResults, []string{"res-1"}) {
		t.Fatalf("decoded cited results = %v", decoded.CitedResults)
	}
}
