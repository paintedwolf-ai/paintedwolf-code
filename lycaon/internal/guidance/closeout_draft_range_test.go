package guidance_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
)

func TestDraftRangeValidation(t *testing.T) {
	t.Parallel()
	contiguous := ledgerOf(readMessages("a.go", lineTexts(42, 45)))
	gapped := ledgerOf(readMessages("a.go", lineTexts(10, 12), lineTexts(20, 22)))
	abutting := ledgerOf(readMessages("a.go", lineTexts(10, 12), lineTexts(13, 15)))
	twoReads := ledgerOf(readMessages("a.go", lineTexts(1, 5)), readMessages("a.go", lineTexts(100, 110)))
	cases := []struct {
		name     string
		source   string
		ev       evidence.Ledger
		want     guidance.CitationOutcome
		wantLine int
		wantEnd  int
	}{
		{"AC-AST-01 unsupported range", "[read#1:42-58]", contiguous, guidance.CitationUnverifiable, 42, 58},
		{"AC-AST-01 unsupported path range", "[a.go:42-58]", contiguous, guidance.CitationUnverifiable, 42, 58},
		{"covered handle range", "[read#1:42-45]", contiguous, guidance.CitationGrounded, 42, 45},
		{"covered path range", "[a.go:43-44]", contiguous, guidance.CitationGrounded, 43, 44},
		{"range starts before observation", "[read#1:41-45]", contiguous, guidance.CitationUnverifiable, 41, 45},
		{"AC-AST-02 range across a gap", "[read#1:11-21]", gapped, guidance.CitationUnverifiable, 11, 21},
		{"both endpoints observed but gap", "[a.go:12-20]", gapped, guidance.CitationUnverifiable, 12, 20},
		{"one interval of several", "[read#1:20-22]", gapped, guidance.CitationGrounded, 20, 22},
		{"abutting intervals cover", "[read#1:11-14]", abutting, guidance.CitationGrounded, 11, 14},
		{"path range lands on the record that covers it", "[a.go:101-105]", twoReads, guidance.CitationGrounded, 101, 105},
		{"explicit handle keeps its own record", "[read#1:101-105]", twoReads, guidance.CitationUnverifiable, 101, 105},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := onlyOutcome(t, tc.source, evidence.CitationRoots{}, tc.ev)
			if got.Outcome != tc.want {
				t.Fatalf("outcome = %+v, want %s", got, tc.want)
			}
			if got.Resolution.Line != tc.wantLine || got.Resolution.EndLine != tc.wantEnd {
				t.Fatalf("range = %d-%d, want %d-%d", got.Resolution.Line, got.Resolution.EndLine, tc.wantLine, tc.wantEnd)
			}
			if tc.want == guidance.CitationUnverifiable {
				if got.Code != guidance.SynthCitationUnverifiableCode {
					t.Fatalf("code = %q", got.Code)
				}
				if want := fmt.Sprintf(":%d-%d", tc.wantLine, tc.wantEnd); !strings.Contains(got.Offender, want) {
					t.Fatalf("offender %q lacks the range %q", got.Offender, want)
				}
			}
		})
	}
}

// AC-AST-13: an excerpt shifts both ends and the whole range is checked again.
func TestDraftRangeExcerptAnchoring(t *testing.T) {
	t.Parallel()
	long := ledgerOf(readMessages("a.go", lineTexts(40, 80)))
	repeated := ledgerOf(readMessages("a.go", spanBlock{start: 40, lines: []string{
		"needle in the file", "x", "y", "z", "w", "v", "needle in the file",
	}}))
	gap := ledgerOf(readMessages("a.go", lineTexts(10, 12), lineTexts(14, 20)))
	cases := []struct {
		name          string
		source        string
		ev            evidence.Ledger
		want          guidance.CitationOutcome
		line, end     int
		correctedFrom int
	}{
		{"excerpt inside the range moves nothing", `[read#1:42-50 "content of line number 47"]`, long, guidance.CitationGrounded, 42, 50, 0},
		{"excerpt at the start", `[a.go:42-50 "content of line number 42"]`, long, guidance.CitationGrounded, 42, 50, 0},
		{"drift below the start shifts both ends", `[read#1:42-50 "content of line number 40"]`, long, guidance.CitationGrounded, 40, 48, 42},
		{"drift past the end shifts both ends", `[read#1:42-50 "content of line number 52"]`, long, guidance.CitationGrounded, 44, 52, 42},
		{"far unique path anchor shifts both ends", `[a.go:42-44 "content of line number 70"]`, long, guidance.CitationGrounded, 70, 72, 42},
		{"far anchor past the observation refuses", `[a.go:42-44 "content of line number 80"]`, long, guidance.CitationUnverifiable, 42, 44, 0},
		{"ambiguous excerpt is no match", `[read#1:42-44 "needle in the file"]`, repeated, guidance.CitationUnverifiable, 42, 44, 0},
		{"shift onto a gap refuses", `[read#1:10-12 "content of line number 14"]`, gap, guidance.CitationUnverifiable, 10, 12, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := onlyOutcome(t, tc.source, evidence.CitationRoots{}, tc.ev)
			if got.Outcome != tc.want {
				t.Fatalf("outcome = %+v, want %s", got, tc.want)
			}
			res := got.Resolution
			if res.Line != tc.line || res.EndLine != tc.end || res.LineCorrectedFrom != tc.correctedFrom {
				t.Fatalf("resolution = %+v, want %d-%d from %d", res, tc.line, tc.end, tc.correctedFrom)
			}
		})
	}
}
