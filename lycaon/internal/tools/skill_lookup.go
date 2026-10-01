package tools

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/skills"
)

// SkillLookup ranks the loaded skills against the model's description of the
// work. The session layer supplies the decision engine and records the
// receipt; the tool never sees either.
type SkillLookup func(ctx context.Context, tctx ToolContext, need string, roster []skills.Skill) turnload.LookupOutcome
