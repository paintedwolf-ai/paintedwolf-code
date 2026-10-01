package survey

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
)

func TestEmitWithRollupPassthroughUnderCap(t *testing.T) {
	recs := []evidence.Record{
		{Path: "a.go", Body: []string{"[p] 1: one"}, LineRanges: []evidence.LineRange{{Start: 1, End: 1}}},
		{Path: "b.go", Body: []string{"[p] 2: two"}, LineRanges: []evidence.LineRange{{Start: 2, End: 2}}},
	}
	tagged, emitted, folded := emitWithRollup("p", 10, recs, 40, false)
	if emitted != 2 || folded != 0 || len(tagged) != 2 {
		t.Fatalf("emitted=%d folded=%d len=%d", emitted, folded, len(tagged))
	}
	if tagged[0].Record.Kind == "rollup" {
		t.Fatal("expected line-level emit under cap")
	}
}

func TestEmitWithRollupPathGroupsByHitCount(t *testing.T) {
	recs := make([]evidence.Record, 0, 12)
	for i := 0; i < 5; i++ {
		recs = append(recs, evidence.Record{
			Path: "hot.go", Body: []string{"[p] 1: hot"}, LineRanges: []evidence.LineRange{{Start: 1, End: 1}},
		})
	}
	for i := 0; i < 3; i++ {
		recs = append(recs, evidence.Record{
			Path: "mid.go", Body: []string{"[p] 1: mid"}, LineRanges: []evidence.LineRange{{Start: 1, End: 1}},
		})
	}
	for i := 0; i < 2; i++ {
		recs = append(recs, evidence.Record{
			Path: "cold.go", Body: []string{"[p] 1: cold"}, LineRanges: []evidence.LineRange{{Start: 1, End: 1}},
		})
	}
	tagged, emitted, folded := emitWithRollup("p", 8, recs, 2, false)
	if emitted != 2 || folded != 2 {
		t.Fatalf("emitted=%d folded=%d want 2,2", emitted, folded)
	}
	if tagged[0].Record.Path != "hot.go" {
		t.Fatalf("first path = %q want hot.go", tagged[0].Record.Path)
	}
	if tagged[0].Record.Kind != "rollup" {
		t.Fatalf("kind = %q want rollup", tagged[0].Record.Kind)
	}
	if !strings.Contains(tagged[0].Record.Body[0], "hits=5") {
		t.Fatalf("body = %q", tagged[0].Record.Body[0])
	}
	if tagged[1].Record.Path != "mid.go" {
		t.Fatalf("second path = %q want mid.go", tagged[1].Record.Path)
	}
}

func TestBuildDigestOrientationAndDrill(t *testing.T) {
	ledger := evidence.AssembleLedger([]evidence.Record{
		{Handle: "snap#1", Path: "a/routes.go", Kind: "rollup", Body: []string{"[http] rollup"}},
		{Handle: "snap#2", Path: "a/routes.go", Kind: "rollup", Body: []string{"[http] rollup2"}},
		{Handle: "snap#3", Path: "b/api.go", Kind: "grep", Body: []string{"[http] hit"}},
	})
	summaries := []ProbeSummary{{
		Label: "http_path_literals", Kind: ProbeGrep, MatchCount: 90, Emitted: 2, Folded: 10, Rolled: true,
	}}
	digest := buildDigest("api_routes", ".", summaries, ledger, ProbeCoverage{})
	if !strings.Contains(digest, "shape:") {
		t.Fatalf("missing shape line: %s", digest)
	}
	if !strings.Contains(digest, "raw=90 emitted=2") {
		t.Fatalf("missing raw/emitted: %s", digest)
	}
	if !strings.Contains(digest, "90 candidates → 2 path groups") {
		t.Fatalf("missing rollup probe line: %s", digest)
	}
	if !strings.Contains(digest, "drill: summarize(path=a/routes.go)") {
		t.Fatalf("missing drill: %s", digest)
	}
	if !strings.Contains(digest, "top_paths=a/routes.go:2") {
		t.Fatalf("missing top_paths: %s", digest)
	}
}

func TestBuildDigestCompactsProbePresentationWithExactTotals(t *testing.T) {
	summaries := make([]ProbeSummary, digestProbeLineMax+12)
	for i := range summaries {
		summaries[i] = ProbeSummary{
			Label:         fmt.Sprintf("probe_%03d", i),
			Kind:          ProbeGrep,
			MatchCount:    10,
			Emitted:       2,
			FilesExamined: 3,
		}
	}
	digest := buildDigest("large", ".", summaries, evidence.Ledger{}, ProbeCoverage{})
	if got := strings.Count(digest, " (grep):"); got != digestProbeLineMax {
		t.Fatalf("probe lines = %d want %d", got, digestProbeLineMax)
	}
	if !strings.Contains(digest, "compacted probes=12 candidates=120 emitted=24 errors=0 sampled=0 files_examined=36") {
		t.Fatalf("missing exact compacted totals: %s", digest)
	}
}
