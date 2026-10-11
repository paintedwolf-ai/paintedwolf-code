package toolhost

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/tools"
	skilltools "github.com/lycaon/lycaon/internal/tools/native/skills"
)

// SkillServices owns skill catalog and rendering.
type SkillServices struct {
	skillsReadTool *skilltools.SkillsReadTool
}

func (r *SkillServices) RenderSkillBody(ctx context.Context, tctx tools.ToolContext, sk skills.Skill) (string, error) {
	if r == nil || r.skillsReadTool == nil {
		return "", fmt.Errorf("skills tool not wired")
	}
	return r.skillsReadTool.RenderBody(ctx, tctx, sk)
}

func (r *SkillServices) SetSkillsCatalog(fn func(ctx context.Context, tctx tools.ToolContext) []skills.Skill) {
	if r == nil || r.skillsReadTool == nil {
		return
	}
	r.skillsReadTool.Skills = fn
}

func (r *SkillServices) SetSkillTemplateVars(fn func(ctx context.Context, tctx tools.ToolContext) map[string]any) {
	if r == nil || r.skillsReadTool == nil {
		return
	}
	r.skillsReadTool.TemplateVars = fn
}

func (r *SkillServices) SetSkillPackConfiguration(
	fn func(ctx context.Context, tctx tools.ToolContext, packID string) map[string]any,
) {
	if r == nil || r.skillsReadTool == nil {
		return
	}
	r.skillsReadTool.PackConfiguration = fn
}
