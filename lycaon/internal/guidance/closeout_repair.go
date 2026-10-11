package guidance

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
)

const citationRepairSampleCap = 8

type citationRepairObservation struct {
	Evidence     string `json:"evidence"`
	Tool         string `json:"tool,omitempty"`
	Path         string `json:"path,omitempty"`
	SupersededBy string `json:"superseded_by,omitempty"`
}

type citationRepairTrailer struct {
	CitedEvidence []CoordinatorCitedEvidence `json:"cited_evidence"`
	CitedURLs     []string                   `json:"cited_urls"`
}

// CloseoutRepairHintData pairs accepted citations with repair candidates by path or tool identity.
func CloseoutRepairHintData(roots evidence.CitationRoots, surface string, report CoordinatorCompletionReport, ev CloseoutEvidence, offenders []string) map[string]any {
	data := GroundingHintData(offenders, ev.Ledger)
	retained := citationRepairTrailer{CitedEvidence: []CoordinatorCitedEvidence{}, CitedURLs: []string{}}
	var rejected []CoordinatorCitedEvidence
	for _, citation := range report.CitedEvidence {
		probe := CoordinatorCompletionReport{Synthesis: report.Synthesis, CitedEvidence: []CoordinatorCitedEvidence{citation}}
		if eval := EvaluateCloseoutCitations(roots, surface, probe, ev); eval.Code == "" && !eval.CitationUnverifiable {
			retained.CitedEvidence = append(retained.CitedEvidence, citation)
		} else {
			rejected = append(rejected, citation)
		}
	}
	for _, url := range report.CitedURLs {
		if ev.URLSeen(url) {
			retained.CitedURLs = append(retained.CitedURLs, url)
		}
	}
	if raw, err := json.Marshal(retained); err == nil {
		data["retained_citations"] = string(raw)
	}
	if raw, err := json.Marshal(citationRepairObservations(roots, ev.Ledger, rejected)); err == nil {
		data["repair_observations"] = string(raw)
	}
	return data
}

func citationRepairObservations(roots evidence.CitationRoots, ev evidence.Ledger, rejected []CoordinatorCitedEvidence) []citationRepairObservation {
	out := make([]citationRepairObservation, 0, citationRepairSampleCap)
	seen := make(map[string]bool)
	add := func(rec evidence.Record) {
		if len(out) == citationRepairSampleCap || seen[rec.Handle] || rec.IsGate() {
			return
		}
		seen[rec.Handle] = true
		out = append(out, citationRepairObservation{Evidence: rec.Handle, Tool: rec.SourceTool, Path: rec.Path, SupersededBy: rec.SupersededBy})
	}
	handles := evidence.HandlesSorted(ev)
	for _, citation := range rejected {
		if rec, known := evidence.ResolveHandle(ev, citation.Evidence); known {
			add(rec)
			if replacement, ok := evidence.ResolveHandle(ev, rec.SupersededBy); ok {
				add(replacement)
			}
		}
		kind, _ := evidence.ParseHandleOrdinal(strings.TrimSpace(citation.Evidence))
		path := evidence.LedgerPathKey(roots, citation.Path, ev.ByPath)
		for i := len(handles) - 1; i >= 0; i-- {
			rec := ev.Handles[handles[i]]
			if rec.SupersededBy != "" {
				continue
			}
			if (kind != "" && (rec.Kind == kind || rec.SourceTool == kind)) || (path != "" && rec.Path == path) {
				add(rec)
			}
		}
	}
	return out
}

// citedHandleRangeLimit bounds the citations a refusal describes.
const citedHandleRangeLimit = 8

// CitedHandleRanges describes rejected provenance and, when unique, the captured
// source observation that matches it. Suggestions never change the report.
func CitedHandleRanges(resolutions []evidence.Resolution, ev evidence.Ledger) string {
	var parts []string
	seen := map[string]bool{}
	for _, res := range resolutions {
		if res.Verdict != evidence.VerdictUnverifiable {
			continue
		}
		part := workerCitationRepairObservation(res, ev)
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		parts = append(parts, part)
		if len(parts) == citedHandleRangeLimit {
			break
		}
	}
	return strings.Join(parts, "; ")
}

func workerCitationRepairObservation(res evidence.Resolution, ev evidence.Ledger) string {
	var observation string
	if rec, ok := evidence.ResolveHandle(ev, res.Handle); ok {
		if len(rec.LineRanges) > 0 {
			ranges := make([]string, 0, len(rec.LineRanges))
			for _, r := range rec.LineRanges {
				ranges = append(ranges, fmt.Sprintf("%d-%d", r.Start, r.End))
			}
			observation = fmt.Sprintf("%s %s lines %s", res.Handle, rec.Path, strings.Join(ranges, ","))
		} else {
			observation = fmt.Sprintf("%s shape %s", res.Handle, evidence.RecordShape(rec))
		}
	}
	if handle := matchingSourceObservation(res, ev); handle != "" {
		match := fmt.Sprintf("%s:%d excerpt matches %s; use that handle in evidence for this source citation", res.Path, res.Line, handle)
		if observation != "" {
			return observation + "; " + match
		}
		return match
	}
	return observation
}

func matchingSourceObservation(res evidence.Resolution, ev evidence.Ledger) string {
	if res.Path == "" || res.Line <= 0 || strings.TrimSpace(res.Excerpt) == "" {
		return ""
	}
	var match string
	for _, handle := range ev.ByPath[res.Path] {
		rec, ok := evidence.ResolveHandle(ev, handle)
		if !ok || rec.Survey || evidence.RecordShape(rec) != evidence.ShapeFileRegion {
			continue
		}
		claim := evidence.Claim{Handle: handle, Path: res.Path, Line: res.Line, Excerpt: res.Excerpt}
		if verified, _ := evidence.VerifyRecord(rec, claim); !verified {
			continue
		}
		if match != "" && match != handle {
			return ""
		}
		match = handle
	}
	return match
}
