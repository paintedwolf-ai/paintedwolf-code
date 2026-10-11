package turnadmission

import (
	"context"
	"strings"
)

type promotionHook func(ctx context.Context, projectID string)

// SetPromotion wires turn-gated promotion recovery when a project quiesces.
func (m *Service) SetPromotion(fn promotionHook) {
	if m == nil {
		return
	}
	m.promotionHook = fn
}

func (m *Service) MaybePromote(ctx context.Context, sessionID string) {
	if m == nil || m.promotionHook == nil || m.store == nil {
		return
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return
	}
	projectID := strings.TrimSpace(sess.ProjectID)
	if projectID == "" {
		return
	}
	m.promotionHook(context.WithoutCancel(ctx), projectID)
}
