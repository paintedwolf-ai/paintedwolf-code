package guidance

import (
	"fmt"
	"strconv"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/verification"
)

// DraftFieldSynthesis is the owner of an occurrence in the Markdown narrative.
const DraftFieldSynthesis = "synthesis"

// WorkflowRunReportDocument is a workflow report's structured document.
type WorkflowRunReportDocument struct {
	Headline  string                `json:"headline,omitempty"`
	Summary   string                `json:"summary,omitempty"`
	Rating    *CoordinatorRating    `json:"rating,omitempty"`
	Findings  []CoordinatorFinding  `json:"findings,omitempty"`
	Limits    []string              `json:"limits,omitempty"`
	Ask       *CoordinatorAsk       `json:"ask,omitempty"`
	SetAsides []CoordinatorSetAside `json:"set_asides,omitempty"`
}

// DraftOccurrence is one protocol reference and the field that owns it. Repeated
// mentions of a handle stay separate occurrences, because they can have different
// coordinates or need different repairs.
type DraftOccurrence struct {
	citationmd.Occurrence
	Owner string
	// RepairID is the host's name for a citation occurrence during repair.
	RepairID string
}

// CloseoutDraft is an assistant response's parsed, typed form. Synthesis keeps
// its citation nodes until presentation. CitedEvidence and CitedURLs are bounded
// display metadata; validation reads every entry of Occurrences.
type CloseoutDraft struct {
	Synthesis     string
	Occurrences   []DraftOccurrence
	CitedEvidence []CoordinatorCitedEvidence
	CitedURLs     []string
	ArtifactIDs   []string
	Verification  *verification.Assessment
	Document      *WorkflowRunReportDocument
	Issues        []citationmd.Issue
	Parsed        citationmd.Document
}

// HasAnswer reports whether the draft states anything once protocol nodes go.
func (d CloseoutDraft) HasAnswer() bool { return citationmd.Narrative(d.Parsed) != "" }

// CitationCount counts model-supplied source references, document ones included.
func (d CloseoutDraft) CitationCount() int {
	count := 0
	for _, occurrence := range d.Occurrences {
		switch occurrence.Kind {
		case citationmd.KindCitation, citationmd.KindWorkspaceLink, citationmd.KindWebLink:
			count++
		case citationmd.KindArtifact, citationmd.KindVerify, citationmd.KindRepair:
		}
	}
	return count
}

// Report is the draft's flat typed view. Synthesis keeps its citation nodes.
func (d CloseoutDraft) Report() CoordinatorCompletionReport {
	report := CoordinatorCompletionReport{
		Verification:  d.Verification,
		Synthesis:     d.Synthesis,
		CitedEvidence: d.CitedEvidence,
		CitedURLs:     d.CitedURLs,
		ArtifactIDs:   d.ArtifactIDs,
	}
	if doc := d.Document; doc != nil {
		report.Headline, report.Summary, report.Rating = doc.Headline, doc.Summary, doc.Rating
		report.Findings, report.Limits, report.Ask, report.SetAsides = doc.Findings, doc.Limits, doc.Ask, doc.SetAsides
	}
	report.Normalize()
	return report
}

// setDocument attaches a document and adds its typed source references to
// validation. They keep their field path; repairing one returns the fence.
func (d *CloseoutDraft) setDocument(document *WorkflowRunReportDocument) {
	kept := d.Occurrences[:0:0]
	for _, occurrence := range d.Occurrences {
		if occurrence.Owner == DraftFieldSynthesis {
			kept = append(kept, occurrence)
		}
	}
	d.Occurrences, d.Document = kept, document
	if document == nil {
		return
	}
	index := len(d.Parsed.Occurrences)
	for i, finding := range document.Findings {
		for j, where := range finding.Where {
			citation := citationmd.Citation{Handle: where.Evidence, Path: where.Path, Line: where.Line, EndLine: where.EndLine, Excerpt: where.Excerpt}
			d.Occurrences = append(d.Occurrences, DraftOccurrence{
				Occurrence: citationmd.Occurrence{Index: index, Kind: citationmd.KindCitation, Token: citation.Token(), Citation: &citation},
				Owner:      fmt.Sprintf("findings[%d].where[%d]", i, j),
			})
			index++
		}
	}
}

func (d *WorkflowRunReportDocument) normalize() {
	report := CoordinatorCompletionReport{
		Headline: d.Headline, Summary: d.Summary, Rating: d.Rating, Findings: d.Findings,
		Limits: d.Limits, Ask: d.Ask, SetAsides: d.SetAsides,
	}
	report.Normalize()
	d.Headline, d.Summary, d.Findings = report.Headline, report.Summary, report.Findings
	d.Limits, d.Ask, d.SetAsides = report.Limits, report.Ask, report.SetAsides
}

// ParseCloseoutDraft parses Markdown into a typed draft. See DraftParseOptions.
func ParseCloseoutDraft(markdown string, opts citationmd.Options) CloseoutDraft {
	parsed := citationmd.Parse(markdown, opts)
	draft := CloseoutDraft{Synthesis: markdown, Issues: parsed.Issues, Parsed: parsed}
	var cited []CoordinatorCitedEvidence
	var urls, artifacts []string
	for _, occurrence := range parsed.Occurrences {
		item := DraftOccurrence{Occurrence: occurrence, Owner: DraftFieldSynthesis}
		repairID := "c" + strconv.Itoa(occurrence.Index+1)
		switch occurrence.Kind {
		case citationmd.KindCitation, citationmd.KindWorkspaceLink:
			item.RepairID = repairID
			cited = append(cited, citedEvidenceFrom(*occurrence.Citation))
		case citationmd.KindWebLink:
			item.RepairID = repairID
			urls = append(urls, occurrence.Link.Destination)
		case citationmd.KindArtifact:
			artifacts = append(artifacts, occurrence.Artifact)
		case citationmd.KindVerify:
			if draft.Verification == nil {
				draft.Verification = occurrence.Verify
			}
		case citationmd.KindRepair:
		}
		draft.Occurrences = append(draft.Occurrences, item)
	}
	draft.CitedEvidence = normalizeCoordinatorCitedEvidence(cited, maxCoordinatorReportListItems)
	draft.CitedURLs = normalizeReportStringList(urls, maxCoordinatorReportListItems)
	draft.ArtifactIDs = normalizeReportStringList(artifacts, maxCoordinatorReportListItems)
	return draft
}

func citedEvidenceFrom(c citationmd.Citation) CoordinatorCitedEvidence {
	return CoordinatorCitedEvidence{Evidence: c.Handle, Path: c.Path, Line: c.Line, EndLine: c.EndLine, Excerpt: c.Excerpt}
}

// DraftParseOptions are the parse facts a live ledger and its roots supply.
func DraftParseOptions(roots evidence.CitationRoots, ev evidence.Ledger) citationmd.Options {
	return citationmd.Options{KnownPath: KnownLedgerPath(roots, ev), SourceRoot: sourceRootPrefix(roots)}
}

// sourceRootPrefix maps a source link's root id to the ledger's path spelling.
func sourceRootPrefix(roots evidence.CitationRoots) func(string) (string, bool) {
	return func(rootID string) (string, bool) {
		if len(roots.Roots) == 0 {
			return "", true
		}
		for _, root := range roots.Roots {
			// A label on the primary root too: a bare path may land on any root,
			// and this link names one.
			if root.ID == rootID {
				return "@" + root.Label + "/", true
			}
		}
		return "", false
	}
}

// KnownLedgerPath is the structured lookup that makes a bare bracket with no
// coordinate a path citation: it holds only for an observed ledger path key.
func KnownLedgerPath(roots evidence.CitationRoots, ev evidence.Ledger) func(string) bool {
	return func(name string) bool {
		key := evidence.LedgerPathKey(roots, name, ev.ByPath)
		return key != "" && len(ev.ByPath[key]) > 0
	}
}
