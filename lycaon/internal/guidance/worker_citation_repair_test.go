package guidance

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/hostmarker"
)

func sourceObservation(handle, path string, line int, text string) evidence.Record {
	return evidence.Record{Handle: handle, Kind: "read", Shape: evidence.ShapeFileRegion,
		Path: path, LineRanges: []evidence.LineRange{{Start: line, End: line}},
		Body: []string{hostmarker.FormatNumberedLines([]string{text}, line)}}
}

func TestCitationRepairIdentifiesSourceWithoutSubstitutingProvenance(t *testing.T) {
	const path = "fixture.go"
	const excerpt = "return &tls.Config{InsecureSkipVerify: true}"
	for _, wrong := range []evidence.Record{
		{Handle: "scan#2", Kind: "scan", Shape: evidence.ShapeArtifact},
		sourceObservation("read#1", "auth.go", 7, excerpt),
	} {
		t.Run(wrong.Handle, func(t *testing.T) {
			ev := evidence.AssembleLedger([]evidence.Record{wrong, sourceObservation("read#9", path, 7, excerpt)})
			finding := WorkerFindingInput{Evidence: wrong.Handle, Path: path, Line: 7, Excerpt: excerpt}
			eval := EvaluateWorkerCitations(evidence.CitationRoots{}, []WorkerFindingInput{finding}, nil, WorkerNarrativeInput{}, ev)
			if eval.Code != WorkerEvidenceHandleUnknownCode {
				t.Fatalf("mismatched provenance accepted: %+v", eval)
			}
			got := CitedHandleRanges(eval.Resolutions, ev)
			if !strings.Contains(got, "fixture.go:7 excerpt matches read#9") {
				t.Fatalf("missing source repair: %q", got)
			}
			finding.Evidence = "read#9"
			repaired := EvaluateWorkerCitations(evidence.CitationRoots{}, []WorkerFindingInput{finding}, nil, WorkerNarrativeInput{}, ev)
			if repaired.Code != "" {
				t.Fatalf("explicit repair rejected: %+v", repaired)
			}
		})
	}
}

func TestCitationRepairDoesNotGuessSourceEvidence(t *testing.T) {
	const excerpt = "return &tls.Config{InsecureSkipVerify: true}"
	res := evidence.Resolution{Handle: "scan#2", Path: "fixture.go", Line: 7, Excerpt: excerpt, Verdict: evidence.VerdictUnverifiable}
	for name, records := range map[string][]evidence.Record{
		"missing":       nil,
		"wrong path":    {sourceObservation("read#9", "other.go", 7, excerpt)},
		"wrong excerpt": {sourceObservation("read#9", "fixture.go", 7, "return nil")},
		"outside range": {sourceObservation("read#9", "fixture.go", 100, excerpt)},
		"ambiguous":     {sourceObservation("read#9", "fixture.go", 7, excerpt), sourceObservation("read#10", "fixture.go", 7, excerpt)},
		"survey":        {{Handle: "survey#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "fixture.go", Survey: true, Body: []string{excerpt}}},
	} {
		t.Run(name, func(t *testing.T) {
			records = append(records, evidence.Record{Handle: "scan#2", Kind: "scan", Shape: evidence.ShapeArtifact})
			got := CitedHandleRanges([]evidence.Resolution{res}, evidence.AssembleLedger(records))
			if got != "scan#2 shape artifact" {
				t.Fatalf("guessed source repair: %q", got)
			}
		})
	}
}

func TestCitationRepairBoundsAndDeduplicatesFeedback(t *testing.T) {
	var records []evidence.Record
	var resolutions []evidence.Resolution
	for i := range 10 {
		handle := fmt.Sprintf("read#%d", i+1)
		records = append(records, sourceObservation(handle, "f.go", i+1, "return nil"))
		res := evidence.Resolution{Handle: handle, Verdict: evidence.VerdictUnverifiable}
		resolutions = append(resolutions, res, res)
	}
	resolutions = append(resolutions, evidence.Resolution{Handle: "read#1", Verdict: evidence.VerdictMatched})
	got := CitedHandleRanges(resolutions, evidence.AssembleLedger(records))
	if strings.Count(got, "; ") != citedHandleRangeLimit-1 || strings.Contains(got, "read#9 ") {
		t.Fatalf("unbounded feedback: %q", got)
	}
	if got := CitedHandleRanges([]evidence.Resolution{{Verdict: evidence.VerdictUnverifiable}}, evidence.Ledger{}); got != "" {
		t.Fatalf("invented observation: %q", got)
	}
}

func TestCitationRepairSuggestsOnlyCurrentLedgerSource(t *testing.T) {
	const excerpt = "return &tls.Config{InsecureSkipVerify: true}"
	res := evidence.Resolution{Handle: "read#old", Path: "fixture.go", Line: 7, Excerpt: excerpt, Verdict: evidence.VerdictUnverifiable}
	current := evidence.AssembleLedger([]evidence.Record{sourceObservation("read#9", "fixture.go", 7, excerpt)})
	got := CitedHandleRanges([]evidence.Resolution{res}, current)
	if got != "fixture.go:7 excerpt matches read#9; use that handle in evidence for this source citation" {
		t.Fatalf("repair = %q", got)
	}
	if got := CitedHandleRanges([]evidence.Resolution{res}, evidence.Ledger{}); got != "" {
		t.Fatalf("suggested absent prior-job evidence: %q", got)
	}
}
