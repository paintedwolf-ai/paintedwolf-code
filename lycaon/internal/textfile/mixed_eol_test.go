package textfile

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSerializePreservingMixedEOL(t *testing.T) {
	cases := []struct {
		name      string
		beforeRaw string
		draftLF   string
		fallback  string
		want      string
		wantMixed bool
	}{
		{
			name:      "middle_line_edit_preserves_outside_crlf_and_lf",
			beforeRaw: "L1_crlf\r\nTARGET\nL3_lf\n",
			draftLF:   "L1_crlf\nEDITED\nL3_lf\n",
			fallback:  "lf",
			want:      "L1_crlf\r\nEDITED\nL3_lf\n",
			wantMixed: true,
		},
		{
			name:      "multi_hunk_preserves_untouched_interior_crlf",
			beforeRaw: "A\r\nB\nC\r\nD\nE\r\n",
			draftLF:   "A\nB_new\nC\nD_new\nE\n",
			fallback:  "lf",
			want:      "A\r\nB_new\nC\r\nD_new\nE\r\n",
			wantMixed: true,
		},
		{
			// Regression for greedy-skip bug: inserting a repeated line ("sep") in hunk 1
			// must NOT jump ahead to match the later "sep" and skip unchanged lines between hunks.
			name:      "repeated_line_bodies_across_multiple_hunks_never_skip_unchanged_lines",
			beforeRaw: "hdr\r\nhunk1_old\nanchor_mid\r\nkeep_crlf\r\nsep\nkeep_lf\nsep\r\nhunk2_old\nfoot\r\n",
			draftLF:   "hdr\nsep\nanchor_mid\nkeep_crlf\nsep\nkeep_lf\nsep\nhunk2_new\nfoot\n",
			fallback:  "lf",
			want:      "hdr\r\nsep\nanchor_mid\r\nkeep_crlf\r\nsep\nkeep_lf\nsep\r\nhunk2_new\nfoot\r\n",
			wantMixed: true,
		},
		{
			name:      "inserted_line_uses_fallback_crlf",
			beforeRaw: "L1\r\nL2\nL3\r\n",
			draftLF:   "L1\nINSERTED\nL2\nL3\n",
			fallback:  "crlf",
			want:      "L1\r\nINSERTED\r\nL2\nL3\r\n",
			wantMixed: true,
		},
		{
			name:      "uniform_before_uses_fallback",
			beforeRaw: "L1\nL2\nL3\n",
			draftLF:   "L1\nEDITED\nL3\n",
			fallback:  "crlf",
			want:      "L1\r\nEDITED\r\nL3\r\n",
			wantMixed: false,
		},
		{
			name:      "preserves_no_trailing_newline",
			beforeRaw: "L1_crlf\r\nTARGET\nL3_eof",
			draftLF:   "L1_crlf\nEDITED\nL3_eof",
			fallback:  "lf",
			want:      "L1_crlf\r\nEDITED\nL3_eof",
			wantMixed: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, mixed, err := SerializePreservingMixedEOL(tc.beforeRaw, tc.draftLF, tc.fallback)
			if err != nil {
				t.Fatalf("SerializePreservingMixedEOL() unexpected error: %v", err)
			}
			if got != tc.want || mixed != tc.wantMixed {
				t.Fatalf("SerializePreservingMixedEOL() = (%q, %v), want (%q, %v)", got, mixed, tc.want, tc.wantMixed)
			}
		})
	}
}

func TestSerializePreservingMixedEOLBoundedWorkOnLargeRewrittenInterior(t *testing.T) {
	const interiorLines = 4000
	var before, draft strings.Builder
	before.WriteString("prefix_crlf\r\n")
	draft.WriteString("prefix_crlf\n")
	for i := 0; i < interiorLines; i++ {
		if i%2 == 0 {
			fmt.Fprintf(&before, "old_line_%04d\r\n", i)
		} else {
			fmt.Fprintf(&before, "old_line_%04d\n", i)
		}
		fmt.Fprintf(&draft, "rewritten_line_%04d\n", i)
	}
	before.WriteString("suffix_crlf\r\n")
	draft.WriteString("suffix_crlf\n")

	got, mixed, steps, err := serializePreservingMixedEOLWithStats(before.String(), draft.String(), "lf")
	if err != nil {
		t.Fatalf("unexpected error on all-rewritten interior: %v", err)
	}
	if !strings.HasPrefix(got, "prefix_crlf\r\n") || !strings.HasSuffix(got, "suffix_crlf\r\n") {
		t.Fatalf("untouched prefix/suffix CRLF not preserved on large rewrite")
	}
	if !mixed {
		t.Fatalf("expected mixed=true with preserved CRLF prefix/suffix and LF rewritten interior")
	}
	// Deterministic O(N + maxD^2) work invariant including failed Myers steps:
	// must stay far below quadratic (4000*4000 = 16,000,000).
	maxAllowedSteps := 12*(interiorLines+2) + (maxMixedEOLMyersD+1)*(maxMixedEOLMyersD+2)
	if steps > maxAllowedSteps {
		t.Fatalf("alignment work %d exceeded bounded limit %d (quadratic regression)", steps, maxAllowedSteps)
	}
}

func TestSerializePreservingMixedEOLUnresolvedSurvivingInteriorFailsClosed(t *testing.T) {
	// 150 replaced lines before + 260 identical alternating interior lines + 150
	// replaced lines after: total edit distance (600) exceeds maxMixedEOLMyersD
	// AND gap cell count (560*560 = 313,600) exceeds maxMixedEOLLCSCells, while
	// 260 original lines survive. Must fail closed instead of silently normalizing.
	const replacedBlock = 150
	const repeatedLines = 260
	var before, draft strings.Builder
	before.WriteString("outer_prefix_crlf\r\n")
	draft.WriteString("outer_prefix_crlf\n")
	for i := 0; i < replacedBlock; i++ {
		fmt.Fprintf(&before, "top_old_%03d\r\n", i)
		fmt.Fprintf(&draft, "top_new_%03d\n", i)
	}
	for i := 0; i < repeatedLines; i++ {
		if i%2 == 0 {
			before.WriteString("repeated_body\r\n")
		} else {
			before.WriteString("repeated_body\n")
		}
		draft.WriteString("repeated_body\n")
	}
	for i := 0; i < replacedBlock; i++ {
		fmt.Fprintf(&before, "bot_old_%03d\n", i)
		fmt.Fprintf(&draft, "bot_new_%03d\n", i)
	}
	before.WriteString("outer_suffix_lf\n")
	draft.WriteString("outer_suffix_lf\n")

	_, _, steps, err := serializePreservingMixedEOLWithStats(before.String(), draft.String(), "lf")
	if !errors.Is(err, ErrMixedEOLAlignmentUnresolved) {
		t.Fatalf("expected ErrMixedEOLAlignmentUnresolved, got %v", err)
	}
	maxAllowedSteps := 12*(2*replacedBlock+repeatedLines+2) + (maxMixedEOLMyersD+1)*(maxMixedEOLMyersD+2)
	if steps > maxAllowedSteps {
		t.Fatalf("unresolved-gap alignment work %d exceeded bounded limit %d", steps, maxAllowedSteps)
	}
}

func TestSerializePreservingMixedEOLRepeatedInteriorAlternatingTerminators(t *testing.T) {
	// 260 identical interior lines (> sqrt(maxMixedEOLLCSCells)) with alternating
	// CRLF/LF terminators and no unique interior anchors, where only the two
	// different lines immediately surrounding the run are edited.
	const repeatedLines = 260
	var before, draft, want strings.Builder

	before.WriteString("outer_prefix_crlf\r\n")
	draft.WriteString("outer_prefix_crlf\n")
	want.WriteString("outer_prefix_crlf\r\n")

	before.WriteString("before_top_old\r\n")
	draft.WriteString("before_top_new\n")
	want.WriteString("before_top_new\n")

	for i := 0; i < repeatedLines; i++ {
		term := "\n"
		if i%2 == 0 {
			term = "\r\n"
		}
		before.WriteString("repeated_body")
		before.WriteString(term)
		draft.WriteString("repeated_body\n")
		want.WriteString("repeated_body")
		want.WriteString(term)
	}

	before.WriteString("after_bot_old\n")
	draft.WriteString("after_bot_new\n")
	want.WriteString("after_bot_new\n")

	before.WriteString("outer_suffix_lf\n")
	draft.WriteString("outer_suffix_lf\n")
	want.WriteString("outer_suffix_lf\n")

	got, mixed, err := SerializePreservingMixedEOL(before.String(), draft.String(), "lf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !mixed {
		t.Fatalf("expected mixed=true with preserved alternating interior terminators")
	}
	if got != want.String() {
		gotLines := splitRawLines(got)
		wantLines := splitRawLines(want.String())
		for i := 0; i < len(gotLines) && i < len(wantLines); i++ {
			if gotLines[i] != wantLines[i] {
				t.Fatalf("first mismatch at line %d: got %+v, want %+v", i, gotLines[i], wantLines[i])
			}
		}
		t.Fatalf("line count mismatch: got %d, want %d", len(gotLines), len(wantLines))
	}
}
