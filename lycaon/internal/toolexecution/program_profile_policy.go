package toolexecution

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// FreezeCommandProfile delegates to the same profile boundary used by normal
// tool approval. Freezing declarations never suppresses a reached approval.
func (p *ApprovalPolicyEngine) FreezeCommandProfile(ctx context.Context, profileID string) (context.Context, toolcontract.CommandEffects, error) {
	if p == nil || p.profile == nil {
		return ctx, 0, fmt.Errorf("command profile policy required")
	}
	return p.profile.FreezeCommandProfile(ctx, profileID)
}
