package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/promptresult"
	"strings"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/pkg/api"
)

// SetAuthzSealer wires the run-start authorization context sealer (fail-closed).
func (m *Manager) SetAuthzSealer(s *authzcontext.Sealer) {
	if m == nil {
		return
	}
	m.authzSealer = s
	m.authzSealRequired = s != nil
}

// AuthzSealWired reports whether production authz seal wiring was applied.
func (m *Manager) AuthzSealWired() bool {
	if m == nil {
		return false
	}
	return m.authzSealRequired && m.authzSealer != nil
}

func (m *Manager) sealAuthorizationContext(ctx context.Context, sess *api.Session, profileID, workerJobID string) error {
	if m == nil || sess == nil {
		return nil
	}
	if m.authzSealer == nil {
		if m.authzSealRequired {
			return authzledger.ErrSealFailed
		}
		return nil
	}
	if strings.TrimSpace(sess.ID) == "" {
		return authzledger.ErrSealFailed
	}
	return m.authzSealer.Seal(ctx, sess, profileID, workerJobID)
}

func (m *Manager) sealFailureResponse(ctx context.Context, sessionID string, err error) (*promptresult.Result, error) {
	if m != nil {
		m.Emit(ctx, sessionID, anchor.AuthzSealFailed, anchor.Envelope{})
	}
	return nil, err
}
