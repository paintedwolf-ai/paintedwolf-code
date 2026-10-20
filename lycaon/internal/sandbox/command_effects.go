package sandbox

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// CommandEffects reads the same effective profile snapshot as tool and path
// checks. An absent declaration admits no program operation.
func (b *Boundary) CommandEffects(ctx context.Context, profileID string) (toolcontract.CommandEffects, error) {
	if b == nil || profileID == "" {
		return 0, fmt.Errorf("explicit command profile required")
	}
	profile, ok := b.lookupProfile(ctx, profileID)
	if !ok {
		return 0, fmt.Errorf("unknown tool profile %q", profileID)
	}
	return profile.CommandEffects, nil
}

// CommandOperationAllowed preserves explicit profile denies across both retained
// operation identities and public command forms without requiring JSON schemas.
func (b *Boundary) CommandOperationAllowed(ctx context.Context, profileID, identity, command string) (bool, error) {
	profile, ok := b.lookupProfile(ctx, profileID)
	if !ok {
		return false, fmt.Errorf("unknown tool profile %q", profileID)
	}
	if profile.ToolDenied(identity) {
		return false, nil
	}
	identities, err := toolcontract.CommandPolicyIdentities(command)
	if err != nil {
		return false, err
	}
	for _, candidate := range identities {
		if profile.ToolDenied(candidate) {
			return false, nil
		}
	}
	return true, nil
}
