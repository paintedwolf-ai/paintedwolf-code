package guidance

import "strings"

const DraftVersionSupersededCode = "DRAFT_VERSION_SUPERSEDED"

// DraftVersionOutcomeLabel resolves a structured outcome_code to short UI copy for
// draft version tabs. Blank and unknown codes use the catalog fallback.
func DraftVersionOutcomeLabel(hints *HintConfig, code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return draftVersionFallbackLabel(hints)
	}
	if hints != nil {
		if entry, ok := hints.HintCodes[code]; ok {
			if label := strings.TrimSpace(entry.DraftOutcomeLabel); label != "" {
				return label
			}
			if label := strings.TrimSpace(entry.Message); label != "" {
				return label
			}
		}
	}
	return draftVersionFallbackLabel(hints)
}

func draftVersionFallbackLabel(hints *HintConfig) string {
	if hints != nil {
		if entry, ok := hints.HintCodes[DraftVersionSupersededCode]; ok {
			if label := strings.TrimSpace(entry.Message); label != "" {
				return label
			}
		}
	}
	return "Superseded"
}
