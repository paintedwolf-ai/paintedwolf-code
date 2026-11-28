package evidence

import "testing"

func TestResolveAmbiguousDifferingBytesAndBoundStaleOrdinal(t *testing.T) {
	t.Parallel()

	roots := CitationRoots{ProjectDir: "/tmp/r02-04-06-test"}

	t.Run("identical_bytes_reread_stays_grounded_and_picks_newest", func(t *testing.T) {
		t.Parallel()
		ev := AssembleLedger([]Record{
			{
				Handle:     "read#1",
				Kind:       "read",
				Shape:      ShapeFileRegion,
				Fidelity:   FidelityStructured,
				SourceTool: "read",
				Path:       "internal/dup.go",
				LineRanges: []LineRange{{Start: 10, End: 12}},
				Body:       []string{"10\tpackage dup", "11\tfunc Value() string { return \"same\" }", "12\t"},
			},
			{
				Handle:     "read#2",
				Kind:       "read",
				Shape:      ShapeFileRegion,
				Fidelity:   FidelityStructured,
				SourceTool: "read",
				Path:       "internal/dup.go",
				LineRanges: []LineRange{{Start: 10, End: 12}},
				Body:       []string{"10\tpackage dup", "11\tfunc Value() string { return \"same\" }", "12\t"},
			},
		})
		res := Resolve(roots, Triple{Path: "internal/dup.go", Line: 11}, ev, "")
		if !res.Verdict.Grounded() || res.Verdict != VerdictTraced || res.Handle != "read#2" {
			t.Fatalf("identical-bytes re-read = %+v, want grounded/traced on newest handle read#2", res)
		}
	})

	t.Run("differing_bytes_at_cited_line_returns_ambiguous", func(t *testing.T) {
		t.Parallel()
		ev := AssembleLedger([]Record{
			{
				Handle:     "read#1",
				Kind:       "read",
				Shape:      ShapeFileRegion,
				Fidelity:   FidelityStructured,
				SourceTool: "read",
				Path:       "internal/dup.go",
				LineRanges: []LineRange{{Start: 10, End: 12}},
				Body:       []string{"10\tpackage dup", "11\tfunc Value() string { return \"old\" }", "12\t"},
			},
			{
				Handle:     "read#2",
				Kind:       "read",
				Shape:      ShapeFileRegion,
				Fidelity:   FidelityStructured,
				SourceTool: "read",
				Path:       "internal/dup.go",
				LineRanges: []LineRange{{Start: 10, End: 12}},
				Body:       []string{"10\tpackage dup", "11\tfunc Value() string { return \"new\" }", "12\t"},
			},
		})
		res := Resolve(roots, Triple{Path: "internal/dup.go", Line: 11}, ev, "")
		if res.Verdict != VerdictAmbiguous || res.Verdict.Grounded() || res.Handle != "" {
			t.Fatalf("differing-bytes ambiguity = %+v, want ambiguous with empty handle", res)
		}
	})

	t.Run("stale_ordinal_hint_adopted_to_unique_record_returns_bound", func(t *testing.T) {
		t.Parallel()
		ev := AssembleLedger([]Record{
			{
				Handle:     "read#1",
				Kind:       "read",
				Shape:      ShapeFileRegion,
				Fidelity:   FidelityStructured,
				SourceTool: "read",
				Path:       "internal/auth/token.go",
				LineRanges: []LineRange{{Start: 20, End: 22}},
				Body:       []string{"20\tfunc Valid() bool {", "21\t\treturn true", "22\t}"},
			},
		})
		res := Resolve(roots, Triple{Path: "internal/auth/token.go", Line: 21, Excerpt: "return true"}, ev, "read#99")
		if res.Verdict != VerdictBound || res.Handle != "read#1" {
			t.Fatalf("stale ordinal R02-06 = %+v, want bound on read#1", res)
		}
		// Exact matching handle hint stays matched.
		exact := Resolve(roots, Triple{Path: "internal/auth/token.go", Line: 21, Excerpt: "return true"}, ev, "read#1")
		if exact.Verdict != VerdictMatched || exact.Handle != "read#1" {
			t.Fatalf("exact handle hint = %+v, want matched on read#1", exact)
		}
	})

	t.Run("best_handle_fallback_with_mismatched_hint_stays_traced", func(t *testing.T) {
		t.Parallel()
		ev := AssembleLedger([]Record{
			{
				Handle:     "read#1",
				Kind:       "read",
				Shape:      ShapeFileRegion,
				Fidelity:   FidelityStructured,
				SourceTool: "read",
				Path:       "internal/auth/token.go",
				LineRanges: []LineRange{{Start: 20, End: 22}},
				Body:       []string{"20\tfunc Valid() bool {", "21\t\treturn true", "22\t}"},
			},
		})
		// Unmatched excerpt falls back to bestHandleForPath: must stay traced, never bound.
		res := Resolve(roots, Triple{Path: "internal/auth/token.go", Line: 21, Excerpt: "paraphrased summary"}, ev, "read#99")
		if res.Verdict != VerdictTraced || res.Handle != "read#1" {
			t.Fatalf("fallback with mismatched hint = %+v, want traced on read#1", res)
		}
	})

	t.Run("colon_prefixed_body_line_not_misparsed_as_line_number", func(t *testing.T) {
		t.Parallel()
		ev := AssembleLedger([]Record{
			{
				Handle:     "read#1",
				Kind:       "read",
				Shape:      ShapeFileRegion,
				Fidelity:   FidelityStructured,
				SourceTool: "read",
				Path:       "internal/server.go",
				LineRanges: []LineRange{{Start: 1, End: 2}},
				Body:       []string{"1\t8080: listen", "2\tfunc Serve() {}"},
			},
			{
				Handle:     "read#2",
				Kind:       "read",
				Shape:      ShapeFileRegion,
				Fidelity:   FidelityStructured,
				SourceTool: "read",
				Path:       "internal/server.go",
				LineRanges: []LineRange{{Start: 1, End: 2}},
				Body:       []string{"1\t8080: listen", "2\tfunc Serve() {}"},
			},
		})
		// Line 1 has identical delivered bytes across both records; "8080: listen"
		// must not be misparsed as line 8080 by hostmarker.ParseNumberedLine.
		res := Resolve(roots, Triple{Path: "internal/server.go", Line: 1}, ev, "")
		if !res.Verdict.Grounded() || res.Handle != "read#2" {
			t.Fatalf("colon-prefixed body line = %+v, want grounded read#2", res)
		}
	})
}
