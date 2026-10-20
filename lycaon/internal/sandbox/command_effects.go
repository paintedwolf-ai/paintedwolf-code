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
