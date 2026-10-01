package inject

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/prompts"
)

func RenderSkillProcedureBlock(ctx context.Context, renderer *prompts.InjectRenderer, sessionID, profileID string, preload turnload.SkillPreload) (string, error) {
	surface := "worker"
	if profileID == prompts.CoordinatorProfileID {
		surface = "coordinator"
	}
	block, err := anchor.RenderInform(ctx, anchor.InjectSkillProcedure,
		anchor.MatchContext{Surface: surface, Profile: profileID, SessionID: sessionID}, renderer,
		map[string]any{"skill_name": preload.Name, "skill_body": strings.TrimSpace(preload.Body), "trigger_tool": preload.Tool})
	return strings.TrimSpace(block), err
}
