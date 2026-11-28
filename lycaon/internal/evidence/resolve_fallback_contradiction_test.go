package evidence_test

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/hostmarker"
)

func numberedDeliveryLedger(t *testing.T, path string, startLine int, lines []string) evidence.Ledger {
	t.Helper()
	obs, err := evidence.ConfirmNumberedWindow(path, hostmarker.FormatNumberedLines(lines, startLine), "resp-1", "deliv-1")
	if err != nil {
		t.Fatalf("ConfirmNumberedWindow: %v", err)
	}
	rec := obs.Record()
	rec.Handle = "read#1"
	return evidence.AssembleLedger([]evidence.Record{rec})
}

func TestResolveFallbackContradiction_GB10F1_FabricatedExcerptFailsClosed(t *testing.T) {
	t.Parallel()
	deliveryEV := numberedDeliveryLedger(t, "src/app.go", 1, []string{
		"package app",
		"func Real() int { return 42 }",
		"func Helper() {}",
	})
	readEV := evidence.AssembleLedger([]evidence.Record{{
		Handle:     "read#1",
		Kind:       "read",
		Shape:      evidence.ShapeFileRegion,
		Fidelity:   evidence.FidelityStructured,
		SourceTool: "read",
		Path:       "src/app.go",
		LineRanges: []evidence.LineRange{{Start: 1, End: 3}},
		Body: []string{
			hostmarker.FormatNumberedLines([]string{
				"package app",
				"func Real() int { return 42 }",
				"func Helper() {}",
			}, 1),
		},
	}})

	cases := []struct {
		name   string
		ev     evidence.Ledger
		triple evidence.Triple
	}{
		{
			name:   "source_delivery_in_range_line_fabricated_excerpt",
			ev:     deliveryEV,
			triple: evidence.Triple{Path: "src/app.go", Line: 2, Excerpt: "func Fabricated()"},
		},
		{
			name:   "source_delivery_path_only_fabricated_excerpt",
			ev:     deliveryEV,
			triple: evidence.Triple{Path: "src/app.go", Excerpt: "func Fabricated()"},
		},
		{
			name:   "read_in_range_line_fabricated_excerpt",
			ev:     readEV,
			triple: evidence.Triple{Path: "src/app.go", Line: 2, Excerpt: "func Fabricated()"},
		},
		{
			name:   "read_path_only_fabricated_excerpt",
			ev:     readEV,
			triple: evidence.Triple{Path: "src/app.go", Excerpt: "func Fabricated()"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := evidence.Resolve(evidence.CitationRoots{}, tc.triple, tc.ev, "")
			if got.Verdict != evidence.VerdictUnverifiable {
				t.Fatalf("verdict = %q (reason=%q), want %q", got.Verdict, got.Reason, evidence.VerdictUnverifiable)
			}
			if got.Reason != evidence.VerifyReasonSourceExcerpt {
				t.Fatalf("reason = %q, want %q", got.Reason, evidence.VerifyReasonSourceExcerpt)
			}
		})
	}
}

func TestResolveFallbackContradiction_GB10F2_UndeliveredLineFailsClosed(t *testing.T) {
	t.Parallel()
	deliveryEV := numberedDeliveryLedger(t, "src/app.go", 1, []string{
		"package app",
		"func Real() int { return 42 }",
		"func Helper() {}",
	})

	for _, line := range []int{4, 90} {
		t.Run(fmt.Sprintf("line_%d", line), func(t *testing.T) {
			t.Parallel()
			got := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{
				Path: "src/app.go",
				Line: line,
			}, deliveryEV, "")
			if got.Verdict != evidence.VerdictUnverifiable {
				t.Fatalf("line %d verdict = %q (reason=%q), want %q", line, got.Verdict, got.Reason, evidence.VerdictUnverifiable)
			}
			if got.Reason != evidence.VerifyReasonSourceRange {
				t.Fatalf("line %d reason = %q, want %q", line, got.Reason, evidence.VerifyReasonSourceRange)
			}
		})
	}
}

func TestResolveFallbackContradiction_NonRegressions(t *testing.T) {
	t.Parallel()
	deliveryEV := numberedDeliveryLedger(t, "src/app.go", 1, []string{
		"package app",
		"func Real() int { return 42 }",
		"func Helper() {}",
	})

	t.Run("in_range_line_without_excerpt_still_traces", func(t *testing.T) {
		t.Parallel()
		got := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{
			Path: "src/app.go",
			Line: 2,
		}, deliveryEV, "")
		if got.Verdict != evidence.VerdictTraced {
			t.Fatalf("verdict = %q (reason=%q), want %q", got.Verdict, got.Reason, evidence.VerdictTraced)
		}
		if got.Handle != "read#1" {
			t.Fatalf("handle = %q, want read#1", got.Handle)
		}
	})

	t.Run("wrong_line_with_unique_live_excerpt_still_reanchors", func(t *testing.T) {
		t.Parallel()
		got := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{
			Path:    "src/app.go",
			Line:    90,
			Excerpt: "func Real() int { return 42 }",
		}, deliveryEV, "")
		if got.Verdict != evidence.VerdictMatched {
			t.Fatalf("verdict = %q (reason=%q), want %q", got.Verdict, got.Reason, evidence.VerdictMatched)
		}
		if got.Line != 2 || got.LineCorrectedFrom != 90 {
			t.Fatalf("re-anchor = line %d (from %d), want line 2 (from 90)", got.Line, got.LineCorrectedFrom)
		}
	})

	t.Run("path_observed_in_full_stays_grounded", func(t *testing.T) {
		t.Parallel()
		bare := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{
			Path: "src/app.go",
		}, deliveryEV, "")
		if !bare.Verdict.Grounded() || bare.Verdict != evidence.VerdictTraced {
			t.Fatalf("bare path = %+v, want grounded traced", bare)
		}
		exact := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{
			Path:    "src/app.go",
			Line:    2,
			Excerpt: "func Real() int { return 42 }",
		}, deliveryEV, "")
		if !exact.Verdict.Grounded() || exact.Verdict != evidence.VerdictMatched {
			t.Fatalf("exact citation = %+v, want grounded matched", exact)
		}
	})
}
