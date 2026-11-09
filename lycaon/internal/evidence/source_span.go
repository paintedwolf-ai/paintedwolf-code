package evidence

import "strings"

const (
	VerifyReasonSourceWindow  = "invalid_source_window"
	VerifyReasonSourcePath    = "source_path_mismatch"
	VerifyReasonSourceRange   = "outside_delivered_range"
	VerifyReasonSourceExcerpt = "source_excerpt_mismatch"
)

func verifyDeliveredSource(rec Record, claim Claim) (bool, string) {
	fact := rec.SourceDelivery
	if fact == nil || fact.ResponseID == "" || fact.DeliveryID == "" || fact.StartLine < 1 || fact.EndLine < fact.StartLine || fact.EndLine-fact.StartLine != len(rec.Body)-1 {
		return false, VerifyReasonSourceWindow
	}
	if claim.Path != "" && NormalizeLedgerPath(claim.Path) != NormalizeLedgerPath(rec.Path) {
		return false, VerifyReasonSourcePath
	}
	excerpt := strings.TrimSpace(claim.Excerpt)
	if claim.Line > 0 {
		if claim.Line < fact.StartLine || claim.Line > fact.EndLine {
			return false, VerifyReasonSourceRange
		}
		if excerpt == "" {
			return true, ""
		}
		index := claim.Line - fact.StartLine
		if strings.Count(excerpt, "\n") >= len(rec.Body)-index {
			return false, VerifyReasonSourceRange
		}
		if deliveredExcerptAt(rec.Body, index, excerpt) {
			return true, ""
		}
		return false, VerifyReasonSourceExcerpt
	}
	if excerpt == "" {
		return true, ""
	}
	for index := range rec.Body {
		if deliveredExcerptAt(rec.Body, index, excerpt) {
			return true, ""
		}
	}
	return false, VerifyReasonSourceExcerpt
}

// The first match must begin on the cited line, within this observation.
func deliveredExcerptAt(lines []string, index int, excerpt string) bool {
	count := 1 + strings.Count(excerpt, "\n")
	if !ExcerptMeaningful(excerpt) || index < 0 || index >= len(lines) || count > len(lines)-index {
		return false
	}
	span := strings.Join(lines[index:index+count], "\n")
	position := strings.Index(span, excerpt)
	return position >= 0 && position <= len(lines[index])
}
