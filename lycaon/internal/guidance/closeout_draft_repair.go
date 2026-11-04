package guidance

import (
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/citationmd"
)

// RepairDefectReason names why a repair was refused. Copy is catalog-owned.
type RepairDefectReason string

const (
	RepairUnknownTarget   RepairDefectReason = "unknown_target"
	RepairStaleTarget     RepairDefectReason = "stale_target"
	RepairDuplicateTarget RepairDefectReason = "duplicate_target"
	RepairWrongKind       RepairDefectReason = "wrong_kind"
	RepairOutsideCycle    RepairDefectReason = "outside_repair"
	RepairCitationBag     RepairDefectReason = "citations_without_targets"
	RepairNarrativeChange RepairDefectReason = "narrative_changed"
)

// RepairDefect is one refused part of a repair. Value is the offending value, or
// empty for a defect of the response as a whole.
type RepairDefect struct {
	Target string
	Value  string
	Reason RepairDefectReason
}

func inactiveRepairDefects(draft CloseoutDraft) []RepairDefect {
	var defects []RepairDefect
	for _, repair := range draftRepairs(draft) {
		defects = append(defects, RepairDefect{Target: repair.Target, Value: repairValue(repair), Reason: RepairOutsideCycle})
	}
	return defects
}

func draftRepairs(draft CloseoutDraft) []citationmd.Repair {
	var repairs []citationmd.Repair
	for _, occurrence := range draft.Occurrences {
		if occurrence.Kind == citationmd.KindRepair {
			repairs = append(repairs, *occurrence.Repair)
		}
	}
	return repairs
}

func repairValue(repair citationmd.Repair) string {
	if repair.Citation != nil {
		return repair.Citation.Token()
	}
	return repair.URL
}

// applyRetainedRepair decides what one retry changes in the retained draft. A
// refused retry leaves the retained draft exactly as it was.
func applyRetainedRepair(next CloseoutDraft, document *WorkflowRunReportDocument, retained RetainedCloseout, opts citationmd.Options) CloseoutRead {
	base := retained.Draft
	refused := func(defects ...RepairDefect) CloseoutRead {
		return CloseoutRead{Draft: base, RepairDefects: defects}
	}
	documentRepair := RepairsReportDocument(retained.ForcedBy)
	narrative := citationmd.Narrative(next.Parsed)
	repairs := draftRepairs(next)
	corrected := false
	switch {
	case len(repairs) > 0:
		patched, defects := applyCitationRepairs(base, repairs, retained, opts)
		if len(defects) > 0 {
			return refused(defects...)
		}
		base = patched
	case narrative == "" && next.CitationCount() > 0:
		return refused(RepairDefect{Reason: RepairCitationBag})
	case narrative == "":
	case narrative == citationmd.Narrative(base.Parsed):
		kept := base.Document
		base = withRetainedDirectives(next, base, opts)
		base.Document = kept
		corrected = true
	case document != nil && documentRepair:
		// A document repair keeps the pinned narrative whatever text surrounds it.
	default:
		return refused(RepairDefect{Reason: RepairNarrativeChange})
	}
	if !corrected {
		base = withDirectives(base, next, opts)
	}
	switch {
	case document == nil:
		base.setDocument(base.Document)
	case documentRepair:
		base.setDocument(document)
	default:
		base.setDocument(withRepairedWhere(base.Document, document))
	}
	return CloseoutRead{Draft: base}
}

// withRepairedWhere takes only finding source references from a citation repair's
// fence, so it cannot rewrite the report's conclusions.
func withRepairedWhere(retained, next *WorkflowRunReportDocument) *WorkflowRunReportDocument {
	if retained == nil {
		return nil
	}
	out := *retained
	out.Findings = slices.Clone(retained.Findings)
	for i := range out.Findings {
		if i < len(next.Findings) && next.Findings[i].ID == out.Findings[i].ID {
			out.Findings[i].Where = next.Findings[i].Where
		}
	}
	return &out
}

// applyCitationRepairs validates every target first, then patches all or none.
func applyCitationRepairs(base CloseoutDraft, repairs []citationmd.Repair, retained RetainedCloseout, opts citationmd.Options) (CloseoutDraft, []RepairDefect) {
	byID := make(map[string]DraftOccurrence, len(base.Occurrences))
	for _, occurrence := range base.Occurrences {
		if occurrence.RepairID != "" {
			byID[occurrence.RepairID] = occurrence
		}
	}
	type patch struct {
		span citationmd.Span
		text string
	}
	var defects []RepairDefect
	var patches []patch
	seen := map[string]bool{}
	for _, repair := range repairs {
		defect := RepairDefect{Target: repair.Target, Value: repairValue(repair)}
		occurrence, known := byID[repair.Target]
		text, kindOK := "", false
		if known {
			text, kindOK = repairText(base.Parsed, occurrence, repair)
		}
		switch {
		case seen[repair.Target]:
			defect.Reason = RepairDuplicateTarget
		case !known && retained.RepairIDs[repair.Target] > 0:
			defect.Reason = RepairStaleTarget
		case !known:
			defect.Reason = RepairUnknownTarget
		case !kindOK:
			defect.Reason = RepairWrongKind
		}
		seen[repair.Target] = true
		if defect.Reason != "" {
			defects = append(defects, defect)
			continue
		}
		patches = append(patches, patch{span: occurrence.Span, text: text})
	}
	if len(defects) > 0 {
		return base, defects
	}
	slices.SortFunc(patches, func(a, b patch) int { return b.span.Start - a.span.Start })
	source := base.Synthesis
	for _, p := range patches {
		source = source[:p.span.Start] + p.text + source[p.span.End:]
	}
	patched := ParseCloseoutDraft(source, opts)
	patched.Document = base.Document
	return patched, nil
}

// repairText is one replacement's source text. ok is false when the value's kind
// cannot stand where the occurrence stands.
func repairText(doc citationmd.Document, occurrence DraftOccurrence, repair citationmd.Repair) (string, bool) {
	switch occurrence.Kind {
	case citationmd.KindCitation:
		if repair.Citation == nil {
			return "", false
		}
		return repair.Citation.Token(), true
	case citationmd.KindWebLink:
		if repair.URL == "" {
			return "", false
		}
		return citationmd.RetargetLink(doc, occurrence.Occurrence, repair.URL), true
	case citationmd.KindWorkspaceLink:
		if repair.Citation == nil || repair.Citation.Path == "" {
			return "", false
		}
		return citationmd.RetargetWorkspaceLink(doc, occurrence.Occurrence, *repair.Citation), true
	case citationmd.KindArtifact, citationmd.KindVerify, citationmd.KindRepair:
	}
	return "", false
}

// withRetainedDirectives keeps directives a full corrected Markdown left out.
func withRetainedDirectives(corrected, retained CloseoutDraft, opts citationmd.Options) CloseoutDraft {
	keep := retained
	if corrected.Verification != nil {
		keep.Verification = nil
	}
	if len(corrected.ArtifactIDs) > 0 {
		keep.ArtifactIDs = nil
	}
	return withDirectives(corrected, keep, opts)
}

// withDirectives applies from's verification and artifact directives to base in
// their ordinary syntax, replacing what base had and any damaged node of that
// kind, so display and metadata derive from one source.
func withDirectives(base, from CloseoutDraft, opts citationmd.Options) CloseoutDraft {
	replaceVerify := from.Verification != nil
	replaceArtifacts := len(from.ArtifactIDs) > 0
	if !replaceVerify && !replaceArtifacts {
		return base
	}
	var cuts []citationmd.Span
	for _, occurrence := range base.Parsed.Occurrences {
		if (occurrence.Kind == citationmd.KindVerify && replaceVerify) || (occurrence.Kind == citationmd.KindArtifact && replaceArtifacts) {
			cuts = append(cuts, occurrence.Node)
		}
	}
	for _, issue := range base.Parsed.Issues {
		if (issue.Kind == citationmd.KindVerify && replaceVerify) || (issue.Kind == citationmd.KindArtifact && replaceArtifacts) {
			cuts = append(cuts, issue.Span)
		}
	}
	slices.SortFunc(cuts, func(a, b citationmd.Span) int { return b.Start - a.Start })
	source := base.Synthesis
	last := -1
	for _, cut := range cuts {
		if cut.Start == last {
			continue
		}
		last = cut.Start
		source = source[:cut.Start] + source[cut.End:]
	}
	var directives []string
	if replaceVerify {
		directives = append(directives, "[verify: "+from.Verification.Method+" - "+from.Verification.Reason+"]")
	}
	for _, id := range from.ArtifactIDs {
		if replaceArtifacts {
			directives = append(directives, "[artifact:"+id+"]")
		}
	}
	out := ParseCloseoutDraft(strings.TrimRight(source, " \t\r\n")+"\n\n"+strings.Join(directives, "\n"), opts)
	out.Document = base.Document
	return out
}
