package chats

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) SetMode(ctx context.Context, id string, mode wire.SessionMode) (*wire.Session, error) {
	sess, err := m.store.SetMode(ctx, strings.TrimSpace(id), mode)
	if errors.Is(err, store.ErrChildSessionMode) {
		return nil, ErrWorkerChildLifecycle
	}
	if err == nil {
		m.publishSessionLifecycle(ctx, sess)
	}
	return sess, err
}
