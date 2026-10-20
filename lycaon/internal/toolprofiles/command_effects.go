package toolprofiles

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// CommandEffects delegates to the executor's profile boundary, retaining its
// invocation snapshot and worker overlays rather than inventing another map.
func (p *ProfilePolicyEngine) CommandEffects(ctx context.Context, profileID string) (toolcontract.CommandEffects, error) {
	if p == nil {
		return 0, fmt.Errorf("command profile policy required")
	}
	source, ok := p.boundary.(interface {
		CommandEffects(context.Context, string) (toolcontract.CommandEffects, error)
	})
	if !ok {
		return 0, fmt.Errorf("profile boundary has no command effect contract")
	}
	return source.CommandEffects(ctx, profileID)
}
