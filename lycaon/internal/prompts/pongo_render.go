package prompts

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/pongoplain"
)

// InjectTemplatePrefix is the ref prefix for system inject partials.
const InjectTemplatePrefix = "inject/"

// GuidanceTemplatePrefix is the ref prefix for tool feedback body templates.
const GuidanceTemplatePrefix = "guidance/"

func (e *FileTemplateEngine) executeTemplate(ctx context.Context, ref string, data map[string]any) (string, error) {
	tpl, err := e.compiledTemplate(ctx, ref)
	if err != nil {
		return "", wrapPongoError(ref, err)
	}
	out, err := pongoplain.Execute(ctx, tpl, withAgentSkillMembershipVars(data))
	if err != nil {
		return "", wrapPongoError(ref, err)
	}
	return out, nil
}

func wrapPongoError(ref string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("template %q: %w", ref, err)
}
