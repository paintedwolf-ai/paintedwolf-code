package evidence

import "testing"

func TestResolveObservationOpaqueAndCommandExcerptInvariant(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		rec   Record
		valid string
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
}
