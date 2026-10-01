package usernotice

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/pongoplain"
)

func renderFieldTemplate(tmpl string, ctx map[string]any) string {
	out, err := renderFieldTemplateChecked(tmpl, ctx)
	if err != nil {
		return ""
	}
	return out
}

func renderFieldTemplateChecked(tmpl string, ctx map[string]any) (string, error) {
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
	out, err := pongoplain.ExecuteDetached(parsed, ctx)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// RenderNoticeCopy renders notice copy.
func RenderNoticeCopy(copy NoticeCopy, ctx map[string]any) NoticeCopy {
	var renderedActions []string
	if len(copy.Actions) > 0 {
		renderedActions = make([]string, 0, len(copy.Actions))
		for _, a := range copy.Actions {
			if strings.Contains(a, "{{") || strings.Contains(a, "{%") {
				rendered := renderFieldTemplate(a, ctx)
				if rendered != "" {
					renderedActions = append(renderedActions, rendered)
				}
			} else if strings.TrimSpace(a) != "" {
				renderedActions = append(renderedActions, a)
			}
		}
	}
	return NoticeCopy{
		Title:           renderFieldTemplate(copy.Title, ctx),
		Message:         renderFieldTemplate(copy.Message, ctx),
		SuggestedAction: renderFieldTemplate(copy.SuggestedAction, ctx),
		Actions:         renderedActions,
	}
}

// RenderCopy renders one registry entry.
func RenderCopy(entry Entry, ctx map[string]any) NoticeCopy {
	return RenderNoticeCopy(noticeCopyFromEntry(entry), ctx)
}

func validatePongoFields(code string, copy NoticeCopy) error {
	for _, field := range []struct {
		name, val string
	}{
		{"title", copy.Title},
		{"message", copy.Message},
		{"suggested_action", copy.SuggestedAction},
	} {
		val := strings.TrimSpace(field.val)
		if val == "" {
			continue
		}
		if !strings.Contains(val, "{{") && !strings.Contains(val, "{%") {
			continue
		}
		if _, err := pongoplain.Compile(val); err != nil {
			return fmt.Errorf("user_notices.%s: %s pongo parse: %w", code, field.name, err)
		}
	}
	return nil
}

func validatePongoRender(code string, copy NoticeCopy, ctx map[string]any) error {
	for _, field := range []struct{ name, val string }{
		{"title", copy.Title},
		{"message", copy.Message},
		{"suggested_action", copy.SuggestedAction},
	} {
		if _, err := renderFieldTemplateChecked(field.val, ctx); err != nil {
			return fmt.Errorf("user_notices.%s: %s pongo render: %w", code, field.name, err)
		}
	}
	return nil
}
