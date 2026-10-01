package guidance

import (
	"fmt"
	"strings"
)

// FormatWorkerSummaryFeedback names the grounding failures and their recovery.
func FormatWorkerSummaryFeedback(hints *HintConfig, code string, data map[string]any) (string, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return "", fmt.Errorf("guidance: empty worker summary code")
	}
	if hints == nil {
		return "", fmt.Errorf("guidance: hint config required for %q", code)
	}
	entry, ok := hints.HintCodes[code]
	if !ok {
		return "", fmt.Errorf("guidance: unknown worker summary code %q", code)
	}
	view := NormalizeRejectCodeView(code, entry)
	var lines []string
	for _, field := range []string{view.What, view.Why, view.Fix} {
		lines = appendDistinctLine(lines, renderFieldTemplate(field, data))
	}
	msg := strings.Join(lines, "\n")
	block := fmt.Sprintf(">>> Worker summary\n%s\nCode: %s", msg, code)
	return block, nil
}

func appendDistinctLine(lines []string, text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return lines
	}
	for _, existing := range lines {
		if existing == text {
			return lines
		}
	}
	return append(lines, text)
}
