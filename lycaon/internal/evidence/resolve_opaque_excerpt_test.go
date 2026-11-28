package evidence

import "testing"

func TestResolveObservationOpaqueAndCommandExcerptInvariant(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		rec    Record
		valid  string
		absent []string
	}{
		{
			name: "command_failure_receipt",
			rec: Record{
				Handle:     "command#1",
				Kind:       "command",
				Shape:      ShapeCommand,
				Fidelity:   FidelityOpaque,
				SourceTool: "command",
				Body:       []string{"status_code: 1", "FAIL: unit_x"},
			},
			valid:  "FAIL: unit_x",
			absent: []string{"ALL TESTS PASSED", "status_code: 0", "PASS: unit_x"},
		},
		{
			name: "opaque_generic_observation",
			rec: Record{
				Handle:     "opaque#1",
				Kind:       "opaque",
				Shape:      ShapeOpaque,
				Fidelity:   FidelityOpaque,
				SourceTool: "tool",
				Body:       []string{"exit=2", "err=permission denied"},
			},
			valid:  "err=permission denied",
			absent: []string{"exit=0", "succeeded cleanly"},
		},
		{
			name: "file_region_without_source_delivery_r03_07",
			rec: Record{
				Handle:     "read#1",
				Kind:       "read",
				Shape:      ShapeFileRegion,
				Fidelity:   FidelityStructured,
				SourceTool: "read",
				Path:       "internal/reanchor.go",
				LineRanges: []LineRange{{Start: 10, End: 12}},
				Body: []string{
					"10\tpackage reanchor",
					"11\t",
					"12\tfunc Real() int { return 42 }",
				},
			},
			valid:  "func Real() int { return 42 }",
			absent: []string{"nonexistent_excerpt", "func Fabricated()"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ev := AssembleLedger([]Record{tc.rec})

			bare := ResolveObservation(CitationRoots{}, ev, tc.rec.Handle, Triple{})
			if bare.Verdict != VerdictTraced {
				t.Fatalf("bare handle %s verdict = %s, want %s", tc.rec.Handle, bare.Verdict, VerdictTraced)
			}

			matched := ResolveObservation(CitationRoots{}, ev, tc.rec.Handle, Triple{Excerpt: tc.valid})
			if matched.Verdict != VerdictMatched {
				t.Fatalf("delivered excerpt %q verdict = %s, want %s", tc.valid, matched.Verdict, VerdictMatched)
			}

			for _, fabricated := range tc.absent {
				got := ResolveObservation(CitationRoots{}, ev, tc.rec.Handle, Triple{Excerpt: fabricated})
				if got.Verdict != VerdictUnverifiable {
					t.Fatalf("absent excerpt %q on %s verdict = %s (reason=%q), want %s", fabricated, tc.rec.Handle, got.Verdict, got.Reason, VerdictUnverifiable)
				}
			}
		})
	}

	t.Run("reanchor_control_preserved_for_unique_live_line", func(t *testing.T) {
		t.Parallel()
		rec := Record{
			Handle:     "read#1",
			Kind:       "read",
			Shape:      ShapeFileRegion,
			Fidelity:   FidelityStructured,
			SourceTool: "read",
			Path:       "internal/reanchor.go",
			LineRanges: []LineRange{{Start: 10, End: 12}},
			Body: []string{
				"10\tpackage reanchor",
				"11\t",
				"12\tfunc Real() int { return 42 }",
			},
		}
		ev := AssembleLedger([]Record{rec})

		// Explicit handle with wrong line 10 and unique live excerpt on line 12:
		// stays grounded (matched) rather than rejected, and path-only Resolve
		// re-anchors line 10 -> 12.
		obs := ResolveObservation(CitationRoots{}, ev, "read#1", Triple{Path: rec.Path, Line: 10, Excerpt: "func Real() int { return 42 }"})
		if obs.Verdict != VerdictMatched {
			t.Fatalf("ResolveObservation unique live excerpt = %+v, want matched", obs)
		}

		// Path-only resolution with wrong line 99 and unique live excerpt on line 12 re-anchors.
		byPath := Resolve(CitationRoots{}, Triple{Path: rec.Path, Line: 99, Excerpt: "func Real() int { return 42 }"}, ev, "")
		if byPath.Verdict != VerdictMatched || byPath.Line != 12 || byPath.LineCorrectedFrom != 99 {
			t.Fatalf("Resolve re-anchor = %+v, want matched line=12 corrected_from=99", byPath)
		}

		// Explicit handle with nonexistent excerpt stays VerdictUnverifiable.
		absentObs := ResolveObservation(CitationRoots{}, ev, "read#1", Triple{Path: rec.Path, Line: 10, Excerpt: "nonexistent_excerpt"})
		if absentObs.Verdict != VerdictUnverifiable {
			t.Fatalf("ResolveObservation absent excerpt = %+v, want unverifiable", absentObs)
		}
	})
}
