package guidance

import (
	"fmt"
	"strings"
)

// GroundingOffenderKey fingerprints a grounding reject for stuck-loop detection.
// Citations-required keys are Code-only: the offender count drifts as the ledger grows.
func GroundingOffenderKey(code string, data map[string]any) string {
	code = strings.TrimSpace(code)
	if CitationsRequiredFamily(code) {
		return code
	}
	if data == nil {
		return code
	}
	sample, _ := data["offenders_sample"].(string)
	count := data["offender_count"]
	return fmt.Sprintf("%s:%v:%s", code, count, sample)
}

// CitationsRequiredFamily identifies missing typed references when the ledger
// contains observations. Its offender key stays Code-only.
func CitationsRequiredFamily(code string) bool {
	switch strings.TrimSpace(code) {
	case InvestCitationsRequiredCode, SynthCitationsRequiredCode:
		return true
	default:
		return false
	}
}

// GroundingRetryBypassesStuckDetection reports codes where the model can fix the
// offender by fetching or searching the URL, so repeating the same URL must not
// exhaust the retry budget early.
func GroundingRetryBypassesStuckDetection(code string) bool {
	switch strings.TrimSpace(code) {
	case InvestURLNotObservedCode, SynthURLNotObservedCode, WorkerURLNotObservedCode:
		return true
	default:
		return false
	}
}
