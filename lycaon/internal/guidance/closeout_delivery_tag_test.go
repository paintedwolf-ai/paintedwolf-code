package guidance

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/hostmarker"
)

func TestCopiedDeliveryTagWithOtherPrintedNumbers(t *testing.T) {
	t.Parallel()
	first := catPlain("read#1", "first.go", 3)
	first.SourceDelivery.LineNumbered = true
	second := catPlain("read#2", "second.go", 3)
	second.SourceDelivery.PrintedOtherNumbers = true
	single := catPlain("read#3", "one line.go", 1)
	single.SourceDelivery.PrintedOtherNumbers = true
	ev := evidence.AssembleLedger([]evidence.Record{first, second, single})
	tag := func(rec evidence.Record) string {
		fact := rec.SourceDelivery
		return fmt.Sprintf("[%s %s]", rec.Handle, hostmarker.FormatEvidenceCoverage(rec.Path, fact.StartLine, fact.EndLine))
	}

	for _, tc := range []struct {
		name, citation string
		want           CitationOutcome
		verdict        evidence.Verdict
	}{
		{"copied range", tag(second), CitationGrounded, evidence.VerdictTraced},
		{"copied single line", tag(single), CitationGrounded, evidence.VerdictTraced},
		{"handle lacks coverage path", "[read#2:1-3]", CitationUnverifiable, evidence.VerdictUnverifiable},
		{"path lacks delivery handle", "[second.go:1-3]", CitationUnverifiable, evidence.VerdictUnverifiable},
		{"subset lacks verified excerpt", "[read#2 second.go:1-2]", CitationUnverifiable, evidence.VerdictUnverifiable},
		{"printed numbers belong to another coordinate system", "[read#2 second.go:4-6]", CitationUnverifiable, evidence.VerdictUnverifiable},
		{"path contradicts handle", "[read#2 first.go:1-3]", CitationUnverifiable, evidence.VerdictUnverifiable},
		{"verified excerpt supplies the coordinate", `[read#2:3 "line 3 of the file"]`, CitationGrounded, evidence.VerdictMatched},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := lineNumberOutcome(t, tc.citation, ev)
			if got.Outcome != tc.want || got.Resolution.Verdict != tc.verdict {
				t.Fatalf("%s outcome = %+v, want %s %s", tc.citation, got, tc.want, tc.verdict)
			}
		})
	}

	ranged := lineNumberOutcome(t, tag(second), ev).Resolution
	if ranged.Handle != second.Handle || ranged.Path != second.Path || ranged.Line != 1 || ranged.EndLine != 3 {
		t.Fatalf("copied tag did not preserve its source range: %+v", ranged)
	}
}
