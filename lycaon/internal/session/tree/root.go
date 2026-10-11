package tree

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

type Reader interface {
	Get(context.Context, string) (*api.Session, error)
}

// RootID resolves a parent chain with a cycle bound.
func RootID(ctx context.Context, store Reader, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if store == nil {
		return sessionID
	}
	for i := 0; sessionID != "" && i < 64; i++ {
		sess, err := store.Get(ctx, sessionID)
		if err != nil || sess == nil {
			return sessionID
		}
		parent := strings.TrimSpace(sess.ParentSessionID)
		if parent == "" {
			return sessionID
		}
		sessionID = parent
	}
	return sessionID
}
