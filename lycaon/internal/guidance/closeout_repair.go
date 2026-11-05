package guidance

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/evidence"
)

const citationRepairSampleCap = 8

type citationRepairObservation struct {
	Evidence     string `json:"evidence"`
	Tool         string `json:"tool,omitempty"`
	Path         string `json:"path,omitempty"`
	SupersededBy string `json:"superseded_by,omitempty"`
	// Lines are the observed ranges. A choice without them takes no coordinate.
	Lines string `json:"lines,omitempty"`
}

// citationRepairKeyField carries a repair refusal's normalized offender key.
const citationRepairKeyField = "citation_repair_key"

// CloseoutRepairHintData names the rejected occurrences by repair ID and offers
// observed choices. Valid occurrences need no mention: the host retains them.
func CloseoutRepairHintData(roots evidence.CitationRoots, draft CloseoutDraft, resolved DraftCitationResolution, ev CloseoutEvidence, offenders []string) map[string]any {
	data := GroundingHintData(offenders, ev.Ledger)
	outcomes := make(map[int]OccurrenceResolution, len(resolved.Occurrences))
	for _, outcome := range resolved.Occurrences {
		outcomes[outcome.OccurrenceIndex] = outcome
	}
	var targets []map[string]any
	var document []string
	var rejected []CoordinatorCitedEvidence
	for _, occurrence := range draft.Occurrences {
		outcome := outcomes[occurrence.Index]
		if !outcome.Rejected() {
			continue
		}
		if occurrence.Citation != nil {
			rejected = append(rejected, citedEvidenceFrom(*occurrence.Citation))
		}
		switch {
		case occurrence.RepairID != "":
			target := map[string]any{
				"id": occurrence.RepairID, "token": occurrence.Token,
				"defect": string(outcome.Outcome), "web_link": occurrence.Kind == citationmd.KindWebLink,
			}
			if occurrence.Citation != nil {
				for key, value := range citationRepairOffer(roots, ev.Ledger, *occurrence.Citation) {
					target[key] = value
				}
			}
			targets = append(targets, target)
		case occurrence.Owner != DraftFieldSynthesis:
			document = append(document, occurrence.Owner+": "+occurrence.Token)
		}
	}
	data["repair_targets"] = targets
	data["document_citation_targets"] = document
	data["retained_document"] = ReportDocumentFence(draft)
	if raw, err := json.Marshal(citationRepairObservations(roots, ev.Ledger, rejected)); err == nil {
		data["repair_observations"] = string(raw)
	}
	return data
}

// citationRepairOffer names a replacement the model can actually send for a refused
// coordinate: the path form when the path's numbered records cover the lines, and
// otherwise the bare handle of its newest record. A coordinate on a worker's record may
// have no other repair, since nothing lets the coordinator re-read for that worker.
func citationRepairOffer(roots evidence.CitationRoots, ev evidence.Ledger, citation citationmd.Citation) map[string]any {
	if citation.Line <= 0 && citation.Path == "" {
		return nil
	}
	path, candidates := resolveWrittenPath(roots, ev, citation.Path)
	if len(candidates) > 0 {
		spelled := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			spelled = append(spelled, "`"+citationmd.Citation{Path: candidate, Line: citation.Line, EndLine: citation.EndLine}.Token()+"`")
		}
		return map[string]any{"candidates": strings.Join(spelled, ", ")}
	}
	if citation.Line <= 0 {
		return nil
	}
	path = evidence.LedgerPathKey(roots, path, ev.ByPath)
	cited, known := evidence.ResolveHandle(ev, citation.Handle)
	if path == "" && known {
		path = cited.Path
	}
	end := max(citation.EndLine, citation.Line)
	var active []evidence.Record
	newest := ""
	handles := ev.ByPath[path]
	for i := len(handles) - 1; i >= 0; i-- {
		rec, ok := evidence.ResolveHandle(ev, handles[i])
		if !ok || rec.SupersededBy != "" || rec.IsGate() {
			continue
		}
		active = append(active, rec)
		if newest == "" && !rec.Survey {
			newest = handles[i]
		}
	}
	if evidence.UnionObserved(active, path, citation.Line, end) {
		return map[string]any{"use": citationmd.Citation{Path: path, Line: citation.Line, EndLine: citation.EndLine}.Token()}
	}
	if newest == "" {
		return nil
	}
	return map[string]any{"use": newest, "worker_output": strings.Contains(newest, ":")}
}

// FindingCitation is a bracket citation for a worker's finding that resolves in the
// union of legs: the coordinate when the record stands behind it, with the excerpt only
// when that is what stands behind it, and otherwise the path.
func FindingCitation(roots evidence.CitationRoots, ev evidence.Ledger, path string, line int, excerpt string) string {
	path, candidates := resolveWrittenPath(roots, ev, path)
	if path == "" || len(candidates) > 0 {
		return ""
	}
	for _, finding := range []WorkerFindingInput{{Path: path, Line: line}, {Path: path, Line: line, Excerpt: excerpt}, {Path: path}} {
		if (finding.Line <= 0 && line > 0 && finding.Excerpt != "") || (finding.Line > 0 && line <= 0) {
			continue
		}
		if classifyDraftFinding(roots, finding, ev).class == closeoutCitationGrounded {
			return "[" + citationmd.Citation{Path: finding.Path, Line: finding.Line, Excerpt: finding.Excerpt}.Token() + "]"
		}
	}
	return ""
}

// CitationRepairRefusalData reports a refused repair. Offenders name the target for
// the model; the key uses only values and reasons, so a changing ID is no new
// offender.
func CitationRepairRefusalData(defects []RepairDefect) map[string]any {
	offenders := make([]string, 0, len(defects))
	keys := make([]string, 0, len(defects))
	for _, defect := range defects {
		parts := []string{}
		if defect.Target != "" {
			parts = append(parts, defect.Target)
		}
		if defect.Value != "" {
			parts = append(parts, defect.Value)
		}
		parts = append(parts, string(defect.Reason))
		offenders = append(offenders, strings.Join(parts, " "))
		keys = append(keys, defect.Value+"|"+string(defect.Reason))
	}
	data := OffenderHintData(offenders)
	data[citationRepairKeyField] = strings.Join(keys, ",")
	return data
}

// CitationSyntaxRefusalData reports parse issues by the source they name.
func CitationSyntaxRefusalData(issues []citationmd.Issue) map[string]any {
	offenders := make([]string, 0, len(issues))
	for _, issue := range issues {
		offenders = append(offenders, issue.Token+" "+string(issue.Code))
	}
	return OffenderHintData(offenders)
}

func citationRepairObservations(roots evidence.CitationRoots, ev evidence.Ledger, rejected []CoordinatorCitedEvidence) []citationRepairObservation {
	out := make([]citationRepairObservation, 0, citationRepairSampleCap)
	seen := make(map[string]bool)
	add := func(rec evidence.Record) {
		if len(out) == citationRepairSampleCap || seen[rec.Handle] || rec.IsGate() {
			return
		}
		seen[rec.Handle] = true
		choice := citationRepairObservation{Evidence: rec.Handle, Tool: rec.SourceTool, Path: rec.Path, SupersededBy: rec.SupersededBy}
		if !rec.Survey && !rec.IsGate() && rec.ShowedLineNumbers() {
			choice.Lines = observedLineRanges(rec)
		}
		out = append(out, choice)
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
	if len(rejected) > 0 && len(out) == 0 {
		for i := len(handles) - 1; i >= 0; i-- {
			rec := ev.Handles[handles[i]]
			if rec.SupersededBy == "" {
				add(rec)
			}
		}
	}
	return out
}

func observedLineRanges(rec evidence.Record) string {
	ranges := make([]string, 0, len(rec.LineRanges))
	for _, r := range rec.LineRanges {
		ranges = append(ranges, fmt.Sprintf("%d-%d", r.Start, r.End))
	}
	return strings.Join(ranges, ",")
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
