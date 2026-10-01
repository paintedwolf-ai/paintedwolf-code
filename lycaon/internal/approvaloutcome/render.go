package approvaloutcome

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/pongoplain"
)

func renderTemplate(tmpl string, ctx map[string]any) string {
	out, err := renderTemplateChecked(tmpl, ctx)
	if err != nil {
		return ""
	}
	return out
}

func renderTemplateChecked(tmpl string, ctx map[string]any) (string, error) {
	tmpl = strings.TrimSpace(tmpl)
	if tmpl == "" {
		return tmpl, nil
	}
	if ctx == nil {
		ctx = map[string]any{}
	}
	if !strings.Contains(tmpl, "{{") && !strings.Contains(tmpl, "{%") {
		return tmpl, nil
	}
	parsed, err := pongoplain.Compile(tmpl)
	if err != nil {
		return "", err
	}
	out, err := pongoplain.Execute(context.Background(), parsed, ctx)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// RenderMessage renders one outcome entry.
func RenderMessage(entry Entry, ctx map[string]any) string {
	return renderTemplate(entry.Message, ctx)
}

func validatePongoField(code, val string) error {
	val = strings.TrimSpace(val)
	if val == "" || (!strings.Contains(val, "{{") && !strings.Contains(val, "{%")) {
		return nil
	}
	if _, err := pongoplain.Compile(val); err != nil {
		return fmt.Errorf("approval_outcomes.%s: message pongo parse: %w", code, err)
	}
	return nil
}
