package inject

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/prompts"
)

// RenderSkillPointerBlock renders the one-line note naming the skill a
// turn's first loadable tool call fitted, when the fit was not confident
// enough to read the body unasked.
func RenderSkillPointerBlock(ctx context.Context, renderer *prompts.InjectRenderer, sessionID, profileID string, pointer turnload.SkillRank) (string, error) {
	surface := "worker"
	if profileID == prompts.CoordinatorProfileID {
		surface = "coordinator"
	}
	block, err := anchor.RenderInform(ctx, anchor.InjectSkillPointer,
		anchor.MatchContext{Surface: surface, Profile: profileID, SessionID: sessionID}, renderer,
		map[string]any{"skill_name": pointer.Name})
	return strings.TrimSpace(block), err
}
