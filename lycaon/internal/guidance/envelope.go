package guidance

import (
	"context"
	"maps"
	"strings"

	"github.com/lycaon/lycaon/internal/oarcore"
)

// NextStepsResolver resolves a hint code's checked next steps against the
// occurrence's frozen request. The host wires it where a RuleSet is available;
// a nil resolver or empty resolution leaves the fact unbound ([OAR-FACT-25]).
type NextStepsResolver func(ctx context.Context, code string, data map[string]any) string

// nextStepsFact is the host fact that carries an occurrence's reachable steps.
// Only copy may bind it; no condition reads it.
const nextStepsFact = "paintedwolf.next_steps"

// EnvelopeHintMessage renders the agent-facing hint line for a tool JSON envelope's
// hint_code, falling back to the code when the registry has no row.
func EnvelopeHintMessage(ctx context.Context, cfg *HintConfig, resolve NextStepsResolver, code string, data map[string]any) string {
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
	return renderHintMessage(ctx, code, entry, data, resolve)
}

func renderHintMessage(ctx context.Context, code string, entry HintEntry, data map[string]any, resolve NextStepsResolver) string {
	view := NormalizeRejectCodeView(code, entry)
	data = withHintNextSteps(ctx, code, view, data, resolve)
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

// withHintNextSteps resolves the checked next-steps fact for copy that binds
// it, without mutating the caller's data.
func withHintNextSteps(ctx context.Context, code string, view RejectCodeView, data map[string]any, resolve NextStepsResolver) map[string]any {
	if resolve == nil || !viewBindsNextSteps(view) {
		return data
	}
	next := resolve(ctx, code, data)
	out := maps.Clone(data)
	if out == nil {
		out = map[string]any{}
	}
	out[nextStepsFact] = next
	return out
}

// viewBindsNextSteps reports whether the rendered hint copy displays the fact.
func viewBindsNextSteps(view RejectCodeView) bool {
	for _, text := range []string{view.What, view.Cause, view.Why, view.Fix, view.Instead} {
		if copyBindsFact(text, nextStepsFact) {
			return true
		}
	}
	return false
}

func copyBindsFact(text string, fact string) bool {
	bindings, err := oarcore.ParseCopy(text)
	if err != nil {
		return false
	}
	for _, binding := range bindings {
		if binding.Name == fact {
			return true
		}
	}
	return false
}
