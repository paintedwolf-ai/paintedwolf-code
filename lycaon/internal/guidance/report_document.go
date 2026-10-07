package guidance

import (
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/jsonshape"
	"github.com/lycaon/lycaon/pkg/api"
)

// Codes a closeout's report fields are refused under; a stored report that
// still fails one carries it as a defect. An unreadable fence applies to every
// report surface; the others check a run's report document.
const (
	ReportFenceUnreadableCode      = string(api.CompletionReportDefectCodeFenceUnreadable)
	ReportDocumentInvalidCode      = string(api.CompletionReportDefectCodeDocumentInvalid)
	ReportClaimUnreportedCode      = string(api.CompletionReportDefectCodeClaimUnreported)
	ReportInventoryUnaccountedCode = string(api.CompletionReportDefectCodeInventoryUnaccounted)
)

// maxUnreadFieldSample bounds the unread members one refusal names.
const maxUnreadFieldSample = 8

// IsFatalReportDefect reports whether a defect code represents a fatal condition
// that prevents accepting the report. Missing claims, unaccounted inventory, or
// invalid document structural invariants are fatal; unreadable fence warnings or
// advisory notices are non-fatal when substantive fields are retained.
func IsFatalReportDefect(code string) bool {
	switch code {
	case ReportClaimUnreportedCode, ReportInventoryUnaccountedCode, ReportDocumentInvalidCode:
		return true
	default:
		return false
	}
}

// AllowedReportFenceKeys lists the top-level member names a report fence may carry.
func AllowedReportFenceKeys() []string {
	return append([]string(nil), reportFenceMembers...)
}

// ExtractOffendingKeys returns unique unrecognized member keys from unread issues.
func ExtractOffendingKeys(unread []jsonshape.Issue) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, u := range unread {
		name := strings.TrimSpace(u.Name)
		if name == "" {
			name = strings.TrimSpace(u.Pattern)
		}
		if name != "" {
			if _, ok := seen[name]; !ok {
				seen[name] = struct{}{}
				out = append(out, name)
			}
		}
	}
	return out
}

// ReportFenceUnreadable is the refusal of a report whose JSON has members the
// report does not take; none when every member was read.
func ReportFenceUnreadable(unread []jsonshape.Issue) (ReportDocumentIssue, bool) {
	if len(unread) == 0 {
		return ReportDocumentIssue{}, false
	}
	sample, total := UnreadReportFields(unread)
	return ReportDocumentIssue{
		Code:      ReportFenceUnreadableCode,
		Reason:    "these members are not report fields, or have the wrong type, so the host did not read them",
		Offenders: sample[:min(len(sample), maxUnreadFieldSample)],
		Count:     total,
	}, true
}

// ReportDocumentIssue is one document requirement a run's closeout fails.
type ReportDocumentIssue struct {
	Code   string
	Reason string
	// Offenders sample what the reason is about; Count is how many there are.
	Offenders []string
	Count     int
}

// ReportDocumentDefects records the requirements a stored report failed.
func ReportDocumentDefects(issues []ReportDocumentIssue) []api.CompletionReportDefect {
	if len(issues) == 0 {
		return nil
	}
	out := make([]api.CompletionReportDefect, 0, len(issues))
	for _, issue := range issues {
		out = append(out, api.CompletionReportDefect{
			Code:     api.CompletionReportDefectCode(issue.Code),
			Reason:   issue.Reason,
			Subjects: append([]string(nil), issue.Offenders...),
			Count:    issue.Count,
		})
	}
	return out
}

// ReportDocumentObservation names each code's reject observation, the fact
// its closeout policy matches.
func ReportDocumentObservation(code string) string {
	switch code {
	case ReportFenceUnreadableCode:
		return "report_fence_unreadable"
	case ReportDocumentInvalidCode:
		return "report_document_invalid"
	case ReportClaimUnreportedCode:
		return "report_claim_unreported"
	case ReportInventoryUnaccountedCode:
		return "report_inventory_unaccounted"
	default:
		return ""
	}
}

// RepairsReportDocument reports whether any rejection code on a closeout's
// retry cycle refused its document fields.
func RepairsReportDocument(codes []string) bool {
	for _, code := range codes {
		if ReportDocumentObservation(code) != "" {
			return true
		}
	}
	return false
}

// ReportDocumentFence renders the report fields the host read from a closeout
// draft, every field but its prose, as the fence a repair returns whole.
func ReportDocumentFence(drafted string) string {
	read, ok := ReadCloseoutReport(drafted, "")
	if !ok {
		return "{}"
	}
	raw, err := json.Marshal(read.Report)
	if err != nil {
		return "{}"
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "{}"
	}
	delete(fields, "synthesis")
	out, err := json.Marshal(fields)
	if err != nil {
		return "{}"
	}
	return string(out)
}
