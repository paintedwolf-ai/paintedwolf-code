package tools

import (
	"github.com/lycaon/lycaon/internal/commandsurface"
)

// CommandPlan fixes the command grammar before inserting protected values.
func (c ToolContext) CommandPlan(args map[string]any) (commandsurface.Plan, error) {
	canonical := args
	if c.Effects.CanonicalArgs != nil {
		canonical = c.Effects.CanonicalArgs
	}
	return commandsurface.ResolvePlan(canonical, c.Effects.Secrets.Substitute)
}
