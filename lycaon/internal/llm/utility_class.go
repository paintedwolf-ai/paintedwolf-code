package llm

import (
	"strings"
)

// UtilityClass is the product decision for one lite call when the slot is
// down or the attempt fails. The coordinator turn is never a utility class.
type UtilityClass string

const (
	// UtilityClassGift is a nicety (session title, draft project name). Lite
	// down leaves the field unset; the coordinator is not spent on it.
	UtilityClassGift UtilityClass = "gift"
	// UtilityClassQuality improves a projection the turn can already compute
	// (curate). Lite down falls back to the coordinator, then a
	// deterministic truncate.
	UtilityClassQuality UtilityClass = "quality"
	// UtilityClassRequested is a user-asked artifact (commit draft, file
	// briefing). Lite down falls back to the coordinator, then a surfaced error.
	UtilityClassRequested UtilityClass = "requested"
	// UtilityClassOverlay is a background hint (verify detect, warm seed).
	// Lite down skips the work and does not cache a negative result.
	UtilityClassOverlay UtilityClass = "overlay"
	// UtilityClassBackground improves a projection off the latency path.
	// Failure is returned to the calling subsystem, which keeps durable state
	// unchanged and may retry later; it never spends the coordinator.
	UtilityClassBackground UtilityClass = "background"
)

// ClassForPurpose maps a capture-row purpose onto its product class.
func ClassForPurpose(purpose string) UtilityClass {
	switch strings.TrimSpace(purpose) {
	case "session_title", "project_name":
		return UtilityClassGift
	case "commit_draft", "file_briefing", "approval_rationale":
		return UtilityClassRequested
	case "verify_detect", "warm_seed":
		return UtilityClassOverlay
	case "compaction":
		return UtilityClassBackground
	case "curate":
		return UtilityClassQuality
	default:
		return UtilityClassQuality
	}
}

// AllowsCoordinatorFallback reports whether a failed or skipped lite attempt
// may spend the coordinator model.
func (c UtilityClass) AllowsCoordinatorFallback() bool {
	return c == UtilityClassQuality || c == UtilityClassRequested
}

// AllowsTruncateFallback reports whether a deterministic truncation may stand
// in for a model summary.
func (c UtilityClass) AllowsTruncateFallback() bool {
	return c == UtilityClassQuality
}

// ReturnsFailureToCaller preserves background errors for transactional handling.
func (c UtilityClass) ReturnsFailureToCaller() bool {
	return c == UtilityClassBackground
}
