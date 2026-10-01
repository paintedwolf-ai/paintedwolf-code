package guidance

import (
	"context"
	"strings"
)

// EnvelopeHintMessage renders the agent-facing hint line for a tool JSON envelope's
// hint_code, falling back to the code when the registry has no row.
func EnvelopeHintMessage(ctx context.Context, cfg *HintConfig, code string, data map[string]any) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	if cfg == nil {
		return code
	}
	entry, ok := cfg.HintCodes[code]
	if !ok {
		return code
	}
	return renderHintMessage(code, entry, data)
}

func renderHintMessage(code string, entry HintEntry, data map[string]any) string {
	view := NormalizeRejectCodeView(code, entry)
	var parts []string
	seen := map[string]bool{}
	for _, field := range []string{view.What, view.Cause, view.Why, view.Fix, view.Instead} {
		text := strings.TrimSpace(renderFieldTemplate(field, data))
		if text != "" && !seen[text] {
			parts = append(parts, text)
			seen[text] = true
		}
	}
	if len(parts) == 0 {
		return code
	}
	return strings.Join(parts, "\n")
}
