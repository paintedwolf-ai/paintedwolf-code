package workflowadmin

import (
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	workflowreview "github.com/lycaon/lycaon/internal/workflow/review"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/report"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// The headline, summary, findings, and limits come from the closeout's
// committed message.
func reportHeadline(msg *wire.Message) string {
	if msg == nil || msg.CompletionReport == nil {
		return ""
	}
	return strings.TrimSpace(msg.CompletionReport.Headline)
}

func reportSummary(msg *wire.Message) string {
	if msg == nil || msg.CompletionReport == nil {
		return ""
	}
	return strings.TrimSpace(msg.CompletionReport.Summary)
}

func reportLimits(msg *wire.Message) []string {
	if msg == nil || msg.CompletionReport == nil {
		return nil
	}
	var out []string
	for _, l := range msg.CompletionReport.Limits {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// assembledFinding pairs a finding's row with the rating answers the closeout
// gave it, and its place in the most-severe-first order.
type assembledFinding struct {
	finding report.ReportFinding
	answers map[string]string
	rank    int
}

// reportFindings are the closeout's own conclusions, most severe first; review
// claims are reported separately. Under a declared rating, a finding that
// needs attention is stated at the level its answers decide, the same level
// the rating counts for it, and rated findings lead.
func reportFindings(msg *wire.Message, brief *workflowdef.Brief, claims []workflowpresentation.RunClaim) []assembledFinding {
	if msg == nil || msg.CompletionReport == nil {
		return nil
	}
	reviewed := workflowreview.ClaimAnswers(claims)
	out := make([]assembledFinding, 0, len(msg.CompletionReport.Findings))
	for _, f := range msg.CompletionReport.Findings {
		title := strings.TrimSpace(f.Title)
		if title == "" {
			continue
		}
		row := report.ReportFinding{
			ID:          strings.TrimSpace(f.ID),
			Title:       title,
			Severity:    strings.TrimSpace(f.Severity),
			Status:      strings.TrimSpace(f.Status),
			Impact:      strings.TrimSpace(f.Impact),
			Action:      strings.TrimSpace(f.Action),
			Disposition: string(f.Disposition),
		}
		for _, w := range f.Where {
			handle, path := strings.TrimSpace(w.Handle), strings.TrimSpace(w.Path)
			if handle == "" && path == "" {
				continue
			}
			row.Where = append(row.Where, report.ReportClaimCitation{Handle: handle, Path: path, Line: w.Line})
		}
		af := assembledFinding{finding: row, answers: f.Answers, rank: report.SeverityRank(row.Severity)}
		if brief != nil {
			af.rank += len(brief.Levels)
			if row.NeedsAttention() {
				answers, _ := ratedAnswers(brief, reviewed, af)
				worst, _ := brief.LevelRange(answers)
				level := brief.Levels[worst]
				af.finding.Severity, af.finding.SeverityTone, af.rank = level.Label, level.Tone, worst
			}
		}
		out = append(out, af)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].rank < out[j].rank })
	return out
}

// ratedAnswers are the answers a finding is rated by: those of a review claim
// sharing its id, else the closeout's own; unreadable answers rate as unknown.
func ratedAnswers(brief *workflowdef.Brief, reviewed map[string]map[string]string, f assembledFinding) (map[string]string, bool) {
	answers, adjudicated := reviewed[f.finding.ID]
	if !adjudicated {
		answers = f.answers
	}
	return brief.Rateable(answers), adjudicated
}

func findingRows(findings []assembledFinding) []report.ReportFinding {
	out := make([]report.ReportFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.finding)
	}
	return out
}

// splitFirstSentence divides a statement into its opening sentence and the
// rest. A statement with no sentence break is its own title.
func splitFirstSentence(statement string) (string, string) {
	statement = strings.Join(strings.Fields(statement), " ")
	if statement == "" {
		return "", ""
	}
	for i, r := range statement {
		if r != '.' && r != '?' && r != '!' {
			continue
		}
		rest := strings.TrimSpace(statement[i+1:])
		if rest == "" {
			return statement, ""
		}
		// A break before a lowercase word is an abbreviation, and one before a
		// digit is inside a number, such as a version.
		if next := []rune(rest)[0]; next >= 'a' && next <= 'z' || next >= '0' && next <= '9' {
			continue
		}
		return strings.TrimSpace(statement[:i+1]), rest
	}
	return statement, ""
}
