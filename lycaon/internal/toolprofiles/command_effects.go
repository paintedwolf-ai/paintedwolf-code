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

// FreezeCommandProfile captures the same complete effective profile for effect
// declarations and operation visibility, including session worker overlays.
func (p *ProfilePolicyEngine) FreezeCommandProfile(ctx context.Context, profileID string) (context.Context, toolcontract.CommandEffects, error) {
	if p == nil {
		return ctx, 0, fmt.Errorf("command profile policy required")
	}
	snapshots, ok := p.boundary.(interface {
		WithToolProfileSnapshot(context.Context, string) (context.Context, error)
	})
	if !ok {
		return ctx, 0, fmt.Errorf("command profile snapshot boundary required")
	}
	frozen, err := snapshots.WithToolProfileSnapshot(ctx, profileID)
	if err != nil {
		return ctx, 0, err
	}
	effects, err := p.CommandEffects(frozen, profileID)
	return frozen, effects, err
}

// CommandOperationAllowed retains host and profile exclusions for a native
// binding while its standalone structured schema may be absent.
func (p *ProfilePolicyEngine) CommandOperationAllowed(ctx context.Context, profileID, identity, command string) (bool, error) {
	if p == nil {
		return false, fmt.Errorf("command profile policy required")
	}
	if denied, _ := p.runtimeDenied(identity); denied {
		return false, nil
	}
	boundary, ok := p.boundary.(interface {
		CommandOperationAllowed(context.Context, string, string, string) (bool, error)
	})
	if !ok {
		return false, fmt.Errorf("native operation profile boundary required")
	}
	return boundary.CommandOperationAllowed(ctx, profileID, identity, command)
}
