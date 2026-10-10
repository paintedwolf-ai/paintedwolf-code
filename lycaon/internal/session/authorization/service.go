package authorization

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/pkg/api"
)

type Service struct {
	authzSealer       *authzcontext.Sealer
	authzSealRequired bool
}

func New() *Service { return &Service{} }

// SetSealer wires the run-start authorization context sealer (fail-closed).
func (m *Service) SetSealer(s *authzcontext.Sealer) {
	if m == nil {
		return
	}
	m.authzSealer = s
	m.authzSealRequired = s != nil
}

// Wired reports whether production authz seal wiring was applied.
func (m *Service) Wired() bool {
	if m == nil {
		return false
	}
	return m.authzSealRequired && m.authzSealer != nil
}

func (m *Service) Seal(ctx context.Context, sess *api.Session, profileID, workerJobID string) error {
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
