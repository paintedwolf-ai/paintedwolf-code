package guidance

import (
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/pkg/api"
)

// BuildObservedAutobindGrounding attaches ledger-bound citation provenance.
func BuildObservedAutobindGrounding(roots evidence.CitationRoots, ev evidence.Ledger, report CoordinatorCompletionReport) *api.CitationGrounding {
	g := &api.CitationGrounding{Traced: false, HostAssembled: true}
	g.CitedEvidence = observedCitedEvidenceForWire(roots, ev, report.CitedEvidence)
	if len(report.CitedURLs) > 0 {
		g.CitedURLs = append([]string(nil), report.CitedURLs...)
	}
	FillObservedSamples(g, ev)
	StampEvidenceRecords(g, ev)
	if report.Verification != nil && report.Verification.Valid() {
		g.Verification = &api.CitationVerification{Method: report.Verification.Method, Reason: report.Verification.Reason}
	}
	return g
}

// observedCitedEvidenceForWire resolves path citations to ledger records.
func observedCitedEvidenceForWire(roots evidence.CitationRoots, ev evidence.Ledger, cited []CoordinatorCitedEvidence) []api.CitationGroundingCitedEvidence {
	if len(cited) == 0 {
		return nil
	}
	out := make([]api.CitationGroundingCitedEvidence, 0, len(cited))
	for _, c := range cited {
		path := normalizedReportPath(roots, c.Path, ev.ByPath)
		if path == "" {
			continue
		}
		handle, ok := evidence.HandleForPath(ev, path)
		if !ok {
			continue
		}
		out = append(out, api.CitationGroundingCitedEvidence{
			Handle:   handle,
			Path:     path,
			Line:     c.Line,
			Excerpt:  strings.TrimSpace(c.Excerpt),
			Verdict:  api.CitationVerdictMatched,
			Openable: evidence.IsOpenablePath(roots, path),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
