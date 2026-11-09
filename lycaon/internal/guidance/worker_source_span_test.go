package guidance

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWorkerCitationsUseDeliveredContiguousSource(t *testing.T) {
	obs, err := evidence.ConfirmNumberedWindow("catalog.py", "    12: def test_catalog():\n    13:     assert catalog() == expected", "response", "delivery")
	if err != nil {
		testutil.FailErr(t, "confirm delivered worker source", err)
	}
	rec := obs.Record()
	rec.Handle = "read#2"
	ev := evidence.AssembleLedger([]evidence.Record{rec})
	for _, tc := range []struct {
		name    string
		finding WorkerFindingInput
		reason  string
	}{
		{"contiguous", WorkerFindingInput{Evidence: "read#2", Path: "catalog.py", Line: 12, Excerpt: "def test_catalog():\n    assert catalog() == expected"}, ""},
		{"wrong path", WorkerFindingInput{Evidence: "read#2", Path: "other.py", Line: 12, Excerpt: "def test_catalog():"}, evidence.VerifyReasonSourcePath},
		{"unseen line", WorkerFindingInput{Evidence: "read#2", Path: "catalog.py", Line: 14, Excerpt: "assert catalog() == expected"}, evidence.VerifyReasonSourceRange},
		{"wrong excerpt", WorkerFindingInput{Evidence: "read#2", Path: "catalog.py", Line: 12, Excerpt: "def another_test():"}, evidence.VerifyReasonSourceExcerpt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eval := EvaluateWorkerCitations(evidence.CitationRoots{}, []WorkerFindingInput{tc.finding}, nil, WorkerNarrativeInput{}, ev)
			if len(eval.Resolutions) != 1 {
				t.Fatalf("missing resolution: %+v", eval)
			}
			res := eval.Resolutions[0]
			if res.Reason != tc.reason || res.Verdict.Grounded() != (tc.reason == "") {
				t.Fatalf("resolution = %+v; want reason %q", res, tc.reason)
			}
			if tc.reason == "" {
				if eval.Code != "" {
					t.Fatalf("valid multiline finding rejected: %+v", eval)
				}
				return
			}
			feedback := CitedHandleRanges(eval.Resolutions, ev)
			if !strings.Contains(feedback, "catalog.py lines 12-13") || !strings.Contains(feedback, "reason="+tc.reason) {
				t.Fatalf("missing observed range or reason: %q", feedback)
			}
		})
	}
}
