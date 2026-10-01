package delegation

import (
	"context"

	"github.com/lycaon/lycaon/internal/session"
)

// ChainedGroundingCoordinator runs delegation grounding before ambient grounding.
type ChainedGroundingCoordinator struct {
	Delegation *GroundingCoordinator
	Ambient    *AmbientGroundingCoordinator
}

var _ session.GroundingHook = (*ChainedGroundingCoordinator)(nil)

// IsEscalated reports whether either delegation or ambient circuit breakers block the session.
func (c *ChainedGroundingCoordinator) IsEscalated(sessionID string) bool {
	if c == nil {
		return false
	}
	if c.Delegation != nil && c.Delegation.IsEscalated(sessionID) {
		return true
	}
	if c.Ambient != nil && c.Ambient.IsEscalated(sessionID) {
		return true
	}
	return false
}

// Reset clears delegation and ambient grounding counters.
func (c *ChainedGroundingCoordinator) Reset(sessionID string) {
	if c == nil {
		return
	}
	if c.Delegation != nil {
		c.Delegation.Reset(sessionID)
	}
	if c.Ambient != nil {
		c.Ambient.Reset(sessionID)
	}
}

// AfterPrompt runs delegation grounding, then ambient when delegation did not apply.
func (c *ChainedGroundingCoordinator) AfterPrompt(ctx context.Context, sessionID string, lastTools []string) error {
	if c == nil {
		return nil
	}
	if c.Delegation != nil {
		if err := c.Delegation.AfterPrompt(ctx, sessionID, lastTools); err != nil {
			return err
		}
	}
	if c.Ambient == nil {
		return nil
	}
	return c.Ambient.AfterPrompt(ctx, sessionID, lastTools)
}
