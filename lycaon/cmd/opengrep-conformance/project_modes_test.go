package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/opengrep"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProjectEvaluationModeSelection(t *testing.T) {
	t.Parallel()
	for _, mode := range []opengrep.Mode{"", opengrep.Intraprocedural, opengrep.Intrafile} {
		got, err := projectEvaluationModes(mode, "")
		testutil.FailErr(t, "select project modes", err)
		want := []opengrep.Mode{mode}
		if mode == "" {
			want = []opengrep.Mode{opengrep.Intraprocedural, opengrep.Intrafile}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("mode %q selects %v, want %v", mode, got, want)
		}
	}
	if _, err := projectEvaluationModes("unknown", ""); err == nil {
		t.Fatal("unknown mode accepted")
	}
	if _, err := projectEvaluationModes(opengrep.Intrafile, opengrep.Intraprocedural); err == nil {
		t.Fatal("admission accepted an unevaluated mode")
	}
}

func TestProjectSummaryOmitsUnevaluatedModes(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	measurements := []projectMeasurement{{Project: "selected", Mode: opengrep.Intrafile, Run: 1}}
	testutil.FailErr(t, "write selected summary", writeSummaries(json.NewEncoder(&output), measurements, "", nil, "development"))
	var summary evaluationSummary
	testutil.FailErr(t, "decode selected summary", json.Unmarshal(output.Bytes(), &summary))
	if summary.Mode != opengrep.Intrafile || summary.Projects != 1 {
		t.Fatalf("unexpected selected-mode summary: %+v", summary)
	}
}
