package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

// EffectiveLimitsForTest returns merged limits.
func (m *Manager) EffectiveLimitsForTest(ctx context.Context, sess *api.Session) settings.SessionLimits {
	return m.effectiveLimits(ctx, sess)
}
