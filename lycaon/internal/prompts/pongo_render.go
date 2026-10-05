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
	out, _, err := e.executeTemplateWithProvenance(ctx, ref, data)
	return out, err
}

func (e *FileTemplateEngine) executeTemplateWithProvenance(ctx context.Context, ref string, data map[string]any) (string, []UnitProvenanceRecord, error) {
	entry, err := e.compiledTemplateEntry(ctx, ref)
	if err != nil {
		return "", nil, wrapPongoError(ref, err)
	}
	out, err := pongoplain.Execute(ctx, entry.tpl, withAgentSkillMembershipVars(data))
	if err != nil {
		return "", nil, wrapPongoError(ref, err)
	}
	prov := append([]UnitProvenanceRecord(nil), entry.provenance...)
	if e.onRender != nil {
		e.onRender(ref, prov)
	}
	return out, prov, nil
}

func wrapPongoError(ref string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("template %q: %w", ref, err)
}
