package guidance

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/verification"
)

const maxCoordinatorReportListItems = 24

// CoordinatorCitedEvidence is one typed citation in a coordinator closeout report.
type CoordinatorCitedEvidence struct {
	Evidence string `json:"evidence,omitempty"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Excerpt  string `json:"excerpt,omitempty"`
}

// CoordinatorCompletionReport is typed coordinator closeout metadata.
type CoordinatorCompletionReport struct {
	Verification  *verification.Assessment   `json:"verification,omitempty"`
	Synthesis     string                     `json:"synthesis"`
	CitedEvidence []CoordinatorCitedEvidence `json:"cited_evidence,omitempty"`
	CitedURLs     []string                   `json:"cited_urls,omitempty"`
	// ArtifactIDs are visual presentation refs.
	ArtifactIDs []string `json:"artifact_ids,omitempty"`

	Headline string `json:"headline,omitempty"`
	Summary  string `json:"summary,omitempty"`
	// Findings precede the narrative in the document.
	Findings []CoordinatorFinding `json:"findings,omitempty"`
	// Limits are the declared coverage gaps, one per entry.
	Limits []string `json:"limits,omitempty"`
	// Ask is the one decision the report asks of its reader.
	Ask *CoordinatorAsk `json:"ask,omitempty"`
	// SetAsides account for scanner groups no finding or claim assessed.
	SetAsides []CoordinatorSetAside `json:"set_asides,omitempty"`
}

// CoordinatorFinding pairs an assessed conclusion with source citations.
type CoordinatorFinding struct {
	ID       string                     `json:"id,omitempty"`
	Title    string                     `json:"title"`
	Severity string                     `json:"severity,omitempty"`
	Status   string                     `json:"status,omitempty"`
	Impact   string                     `json:"impact,omitempty"`
	Action   string                     `json:"action,omitempty"`
	Where    []CoordinatorCitedEvidence `json:"where,omitempty"`
	// Disposition is act (needs work), accept (a risk kept on purpose), or
	// held (a surface examined and found sound).
	Disposition string `json:"disposition,omitempty"`
	// Answers rate the finding against the workflow's declared questions,
	// unless a claim with the same id already carries adjudicated answers.
	Answers map[string]string `json:"answers,omitempty"`
	// ScanGroupIDs are the run's scanner groups this finding assesses.
	ScanGroupIDs []string `json:"scan_group_ids,omitempty"`
}

// CoordinatorAsk is the decision a report asks of its reader, written for
// someone who has never seen the work.
type CoordinatorAsk struct {
	Do     string `json:"do"`
	Effort string `json:"effort"`
	Why    string `json:"why,omitempty"`
}

// CoordinatorSetAside accounts for scanner groups by id or by selector: every
// group a named scanner reported entirely inside the listed path globs.
type CoordinatorSetAside struct {
	ScanGroupIDs []string `json:"scan_group_ids,omitempty"`
	Scanner      string   `json:"scanner,omitempty"`
	Paths        []string `json:"paths,omitempty"`
	Reason       string   `json:"reason"`
}

// CoordinatorCloseoutTranscriptNarrative extracts displayable report prose.
func CoordinatorCloseoutTranscriptNarrative(content string) (string, bool) {
	report, ok := ParseCoordinatorCompletionReport(content)
	if !ok {
		return "", false
	}
	if CloseoutBodyIsEnvelopeShaped(report.Synthesis) {
		return "", false
	}
	return report.Synthesis, true
}

// Normalize trims and caps closeout report fields.
func (r *CoordinatorCompletionReport) Normalize() {
	r.Synthesis = strings.TrimSpace(r.Synthesis)
	r.CitedEvidence = normalizeCoordinatorCitedEvidence(r.CitedEvidence, maxCoordinatorReportListItems)
	r.CitedURLs = normalizeReportStringList(r.CitedURLs, maxCoordinatorReportListItems)
	r.ArtifactIDs = normalizeReportStringList(r.ArtifactIDs, maxCoordinatorReportListItems)
	r.Headline = strings.Join(strings.Fields(r.Headline), " ")
	r.Summary = strings.TrimSpace(r.Summary)
	r.Findings = normalizeCoordinatorFindings(r.Findings, maxCoordinatorReportListItems)
	r.Limits = normalizeReportStringList(r.Limits, maxCoordinatorReportListItems)
	r.Ask = normalizeCoordinatorAsk(r.Ask)
	r.SetAsides = normalizeCoordinatorSetAsides(r.SetAsides)
}

// maxReportGroupIDs bounds the scanner groups one finding or set-aside names.
const maxReportGroupIDs = 400

func normalizeCoordinatorAsk(in *CoordinatorAsk) *CoordinatorAsk {
	if in == nil {
		return nil
	}
	out := &CoordinatorAsk{
		Do:     strings.Join(strings.Fields(in.Do), " "),
		Effort: strings.ToLower(strings.TrimSpace(in.Effort)),
		Why:    strings.Join(strings.Fields(in.Why), " "),
	}
	if out.Do == "" && out.Effort == "" && out.Why == "" {
		return nil
	}
	return out
}

func normalizeCoordinatorSetAsides(in []CoordinatorSetAside) []CoordinatorSetAside {
	if len(in) == 0 {
		return nil
	}
	out := make([]CoordinatorSetAside, 0, len(in))
	for _, sa := range in {
		sa.ScanGroupIDs = normalizeReportStringList(sa.ScanGroupIDs, maxReportGroupIDs)
		sa.Scanner = strings.TrimSpace(sa.Scanner)
		sa.Paths = normalizeReportStringList(sa.Paths, maxCoordinatorReportListItems)
		sa.Reason = strings.Join(strings.Fields(sa.Reason), " ")
		if len(sa.ScanGroupIDs) == 0 && sa.Scanner == "" && len(sa.Paths) == 0 && sa.Reason == "" {
			continue
		}
		out = append(out, sa)
		if len(out) >= maxCoordinatorReportListItems {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizeCoordinatorFindings removes empty entries and flattens their text.
// A finding with an id but no title is kept, so the document check can name
// the missing title instead of reporting the id as never carried.
func normalizeCoordinatorFindings(in []CoordinatorFinding, max int) []CoordinatorFinding {
	if len(in) == 0 {
		return nil
	}
	out := make([]CoordinatorFinding, 0, len(in))
	for _, f := range in {
		f.ID = strings.TrimSpace(f.ID)
		f.Title = strings.Join(strings.Fields(f.Title), " ")
		f.Severity = strings.TrimSpace(f.Severity)
		f.Status = strings.TrimSpace(f.Status)
		f.Impact = strings.Join(strings.Fields(f.Impact), " ")
		f.Action = strings.Join(strings.Fields(f.Action), " ")
		f.Where = normalizeCoordinatorCitedEvidence(f.Where, maxCoordinatorReportListItems)
		f.Disposition = strings.ToLower(strings.TrimSpace(f.Disposition))
		f.Answers = normalizeReportAnswers(f.Answers)
		f.ScanGroupIDs = normalizeReportStringList(f.ScanGroupIDs, maxReportGroupIDs)
		if f.Title == "" && f.ID == "" {
			continue
		}
		out = append(out, f)
		if max > 0 && len(out) >= max {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeReportAnswers(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// MarshalCoordinatorCompletionReport JSON-encodes a closeout report for transcript commit.
func MarshalCoordinatorCompletionReport(report CoordinatorCompletionReport) (string, error) {
	report.Normalize()
	raw, err := json.Marshal(report)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func normalizeCoordinatorCitedEvidence(in []CoordinatorCitedEvidence, max int) []CoordinatorCitedEvidence {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	var out []CoordinatorCitedEvidence
	for _, item := range in {
		handle := strings.TrimSpace(item.Evidence)
		path := strings.TrimSpace(item.Path)
		if handle == "" && path == "" {
			continue
		}
		key := handle + "\x00" + path
		if item.Line > 0 {
			key = fmt.Sprintf("%s:%d", key, item.Line)
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, CoordinatorCitedEvidence{
			Evidence: handle,
			Path:     path,
			Line:     item.Line,
			Excerpt:  strings.TrimSpace(item.Excerpt),
		})
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}

func normalizeReportStringList(in []string, max int) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, dup := seen[item]; dup {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}
