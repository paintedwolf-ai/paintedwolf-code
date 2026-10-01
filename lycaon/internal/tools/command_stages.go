package tools

import (
	"github.com/lycaon/lycaon/internal/commandsurface"
)

// CommandPlan fixes the command grammar before inserting protected values.
func (c ToolContext) CommandPlan(args map[string]any) (commandsurface.Plan, error) {
	canonical := args
	if c.CanonicalArgs != nil {
		canonical = c.CanonicalArgs
	}
	return commandsurface.ResolvePlan(canonical, c.Secrets.Substitute)
}
