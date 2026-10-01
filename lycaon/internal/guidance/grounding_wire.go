package guidance

import (
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/pkg/api"
)

const maxWireEvidenceRecords = 24
const maxWireEvidenceExcerpt = 240

// StampEvidenceRecords projects the ledger onto a grounding record: the capped
// sample plus the ledger size it came from, which is what distinguishes a
// truncated sample from a complete one. Stamp after the citations are set:
// the records they cite fill the sample first.
func StampEvidenceRecords(g *api.CitationGrounding, ev evidence.Ledger) {
	if g == nil {
		return
	}
	g.EvidenceRecords = evidenceRecordsForWire(ev, citedHandles(g))
	g.EvidenceRecordCount = len(ev.Handles)
}

// citedHandles are the ledger handles the grounding's citations resolved to.
func citedHandles(g *api.CitationGrounding) map[string]bool {
	out := map[string]bool{}
	for _, c := range g.CitedEvidence {
		if h := strings.TrimSpace(c.Handle); h != "" {
			out[h] = true
		}
	}
	for _, f := range g.Findings {
		if h := strings.TrimSpace(f.Handle); h != "" {
			out[h] = true
		}
	}
	return out
}

// evidenceRecordsForWire projects ledger records for citation grounding UI,
// cited records first, so uncited ones never crowd them out of the cap.
func evidenceRecordsForWire(ev evidence.Ledger, cited map[string]bool) []api.CitationGroundingEvidenceRecord {
	if len(ev.Handles) == 0 {
		return nil
	}
	handles := evidence.HandlesSorted(ev)
	slices.SortStableFunc(handles, func(a, b string) int {
		switch {
		case cited[a] == cited[b]:
			return 0
		case cited[a]:
			return -1
		default:
			return 1
		}
	})
	out := make([]api.CitationGroundingEvidenceRecord, 0, min(len(handles), maxWireEvidenceRecords))
	for _, handle := range handles {
		rec, ok := ev.Handles[handle]
		if !ok {
			continue
		}
		out = append(out, wireEvidenceRecord(ev, rec))
		if len(out) >= maxWireEvidenceRecords {
			break
		}
	}
	return out
}

func wireEvidenceRecord(ev evidence.Ledger, rec evidence.Record) api.CitationGroundingEvidenceRecord {
	shape := evidence.RecordShape(rec)
	trust := strings.TrimSpace(rec.Fidelity)
	if trust == "" {
		trust = evidence.RecordFidelity(rec, ev, rec.Handle)
	}
	item := api.CitationGroundingEvidenceRecord{
		Handle:    rec.Handle,
		Kind:      rec.Kind,
		Shape:     shape,
		Fidelity:  trust,
		Tool:      strings.TrimSpace(rec.SourceTool),
		Path:      rec.Path,
		URLs:      evidence.ObservedURLsForRecord(rec),
		URLTitles: rec.URLTitles(),
		Truncated: rec.Truncated,
	}
	if len(rec.LineRanges) > 0 {
		item.Line = rec.LineRanges[0].Start
		if end := rec.LineRanges[0].End; end > item.Line {
			item.LineEnd = end
		}
	}
	item.MatchCount, item.MatchPaths = evidence.GrepMatchStats(rec)
	item.Excerpt = wireBodyExcerpt(rec)
	return item
}

func wireBodyExcerpt(rec evidence.Record) string {
	if len(rec.Body) == 0 {
		return ""
	}
	body := strings.TrimSpace(rec.Body[0])
	if len(body) > maxWireEvidenceExcerpt {
		// The cap is a byte offset, so it can land mid-rune.
		return strings.ToValidUTF8(body[:maxWireEvidenceExcerpt], "") + "…"
	}
	return body
}
