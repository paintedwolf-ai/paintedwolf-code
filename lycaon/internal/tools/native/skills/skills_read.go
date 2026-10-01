package skills

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
	"github.com/lycaon/lycaon/pkg/api"
)

// SkillsReadTool renders instructions and reads approved resources.
type SkillsReadTool struct {
	Skills       func(ctx context.Context, tctx tools.ToolContext) []skills.Skill
	TemplateVars func(ctx context.Context, tctx tools.ToolContext) map[string]any
	// PackConfiguration resolves settings for the skill's provider pack.
	PackConfiguration func(ctx context.Context, tctx tools.ToolContext, packID string) map[string]any
	// Lookup ranks described needs; without it, exact names resolve and other
	// text answers with the catalog.
	Lookup tools.SkillLookup
}

func (t *SkillsReadTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	catalog := t.catalog(ctx, tctx)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	resource := ""
	need := ""
	if args != nil {
		if raw, ok := args["resource"].(string); ok {
			resource = strings.TrimSpace(raw)
		}
		if raw, ok := args["need"].(string); ok {
			need = strings.TrimSpace(raw)
		}
	}
	if need == "" {
		return "", skillUnknown(need)
	}
	need, cursor, err := turnload.ParseDiscoveryNeed(need)
	if err != nil {
		return "", tools.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": err.Error()})
	}
	if cursor != "" {
		if resource != "" {
			return "", tools.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": "discovery continuation cannot be combined with resource"})
		}
		return skillDiscovery(tctx, need, cursor, "catalog", "", catalog)
	}
	if resource != "" && turnload.ExactSkill(need, catalog) == "" {
		return "", skillUnknown(need)
	}
	outcome := t.lookup(ctx, tctx, need, catalog)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	names := outcome.Names()
	if len(names) == 0 {
		if len(catalog) == 0 || outcome.Failure == "" {
			return "", skillUnknown(need)
		}
		return skillDiscovery(tctx, need, "", "ranking_unavailable", outcome.Failure, catalog)
	}
	name := names[0]
	for _, sk := range catalog {
		if sk.Name != name {
			continue
		}
		activated := sk
		if resource != "" {
			body, err := sk.ReadResource(resource)
			if err != nil {
				return "", skillResourceUnknown(sk, resource)
			}
			activated.Body = string(body)
			if sk.TemplatesResource(resource) {
				activated.Body, err = t.renderPackBody(ctx, tctx, activated)
				if err != nil {
					return "", err
				}
			}
			stampSkillActivation(tctx, activated)
			if path := sk.SourcePath(resource); path != "" {
				tctx.RecordSourcePath(path, api.NavigationEntryKindFile)
			}
			return formatSkillResource(sk.Name, resource, []byte(activated.Body)), nil
		}
		body, err := t.RenderBody(ctx, tctx, sk)
		if err != nil {
			return "", err
		}
		activated.Body = body
		stampSkillActivation(tctx, activated)
		recordSkillSources(tctx, sk)
		return formatSkillActivation(activated), nil
	}
	return "", skillUnknown(need)
}

// RenderBody returns the instructions a read of sk delivers: project skills
// verbatim, pack skills through their template with policy and pack settings.
func (t *SkillsReadTool) RenderBody(ctx context.Context, tctx tools.ToolContext, sk skills.Skill) (string, error) {
	if sk.Project {
		return sk.Body, nil
	}
	return t.renderPackBody(ctx, tctx, sk)
}

func (t *SkillsReadTool) renderPackBody(ctx context.Context, tctx tools.ToolContext, sk skills.Skill) (string, error) {
	tpl, err := pongoplain.Compile(sk.Body)
	if err != nil {
		return "", skillTemplateInvalid(sk.Name, err)
	}
	vars := map[string]any{}
	for name, value := range t.policyVars(ctx, tctx) {
		vars[name] = value
	}
	// Keep configuration defined for templates without settings.
	vars["configuration"] = t.packConfiguration(ctx, tctx, sk.PackID)
	out, err := pongoplain.Execute(ctx, tpl, vars)
	if err != nil {
		return "", skillTemplateInvalid(sk.Name, err)
	}
	return out, nil
}

func (t *SkillsReadTool) packConfiguration(
	ctx context.Context, tctx tools.ToolContext, packID string,
) map[string]any {
	packID = strings.TrimSpace(packID)
	if t == nil || t.PackConfiguration == nil || packID == "" {
		return map[string]any{}
	}
	values := t.PackConfiguration(ctx, tctx, packID)
	if values == nil {
		return map[string]any{}
	}
	return values
}

func (t *SkillsReadTool) policyVars(ctx context.Context, tctx tools.ToolContext) map[string]any {
	if t != nil && t.TemplateVars != nil {
		if vars := t.TemplateVars(ctx, tctx); vars != nil {
			spawn.RefreshSizingHintVars(vars)
			return vars
		}
	}
	return spawn.PolicyTemplateVars(spawn.DefaultWorkerToolBudget())
}

// stampSkillActivation copies catalog fields onto the tool result for the skill card.
func stampSkillActivation(tctx tools.ToolContext, sk skills.Skill) {
	if tctx.Out == nil {
		return
	}
	tctx.Out.Skill = &api.SkillActivation{
		Name:             sk.Name,
		Description:      sk.Description,
		Instructions:     sk.Body,
		Dir:              sk.Dir,
		Resources:        append([]string(nil), sk.Resources...),
		ResourcesOmitted: sk.ResourcesOmitted,
		Project:          sk.Project,
		PackID:           sk.PackID,
		License:          sk.License,
		Compatibility:    sk.Compatibility,
		AllowedTools:     sk.AllowedTools,
	}
}

func (t *SkillsReadTool) lookup(ctx context.Context, tctx tools.ToolContext, need string, catalog []skills.Skill) turnload.LookupOutcome {
	if t != nil && t.Lookup != nil {
		return t.Lookup(ctx, tctx, need, catalog)
	}
	return turnload.LookupSkills(ctx, nil, turnload.LookupSpec{}, need, catalog)
}

func (t *SkillsReadTool) catalog(ctx context.Context, tctx tools.ToolContext) []skills.Skill {
	if t == nil || t.Skills == nil {
		return nil
	}
	return t.Skills(ctx, tctx)
}

func skillUnknown(need string) error {
	return safecmd.Reject("SKILL_UNKNOWN", map[string]any{
		"need": need,
	})
}

func skillTemplateInvalid(name string, err error) error {
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	return safecmd.Reject("SKILL_TEMPLATE_INVALID", map[string]any{
		"name":   name,
		"detail": detail,
	})
}

func skillResourceUnknown(sk skills.Skill, resource string) error {
	return safecmd.Reject("SKILL_RESOURCE_UNKNOWN", map[string]any{
		"name":      sk.Name,
		"resource":  resource,
		"available": append([]string(nil), sk.Resources...),
	})
}

func formatSkillResource(name, resource string, body []byte) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Skill resource: %s/%s\n\n", name, resource)
	b.Write(body)
	if len(body) == 0 || body[len(body)-1] != '\n' {
		b.WriteByte('\n')
	}
	return b.String()
}

func formatSkillActivation(sk skills.Skill) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Skill: %s\n\n", sk.Name)
	b.WriteString(sk.Body)
	if !strings.HasSuffix(sk.Body, "\n") {
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	fmt.Fprintf(&b, "Skill directory: %s\n", sk.Dir)
	if source := sk.SourcePath(""); source != "" {
		fmt.Fprintf(&b, "Skill file: %s\n", source)
	}
	if len(sk.Resources) == 0 {
		return b.String()
	}
	b.WriteString("Files: ")
	b.WriteString(strings.Join(sk.Resources, ", "))
	if sk.ResourcesOmitted > 0 {
		fmt.Fprintf(&b, " (+%d more)", sk.ResourcesOmitted)
	}
	b.WriteByte('\n')
	return b.String()
}

func recordSkillSources(tctx tools.ToolContext, sk skills.Skill) {
	if path := sk.SourcePath(""); path != "" {
		tctx.RecordSourcePath(path, api.NavigationEntryKindFile)
	}
	for _, resource := range sk.Resources {
		if path := sk.SourcePath(resource); path != "" {
			tctx.RecordSourcePath(path, api.NavigationEntryKindFile)
		}
	}
}
