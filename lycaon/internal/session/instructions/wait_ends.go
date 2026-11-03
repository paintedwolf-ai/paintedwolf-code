package instructions

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/pkg/api"
)

// SetWaitEnds installs the source of waits that ended without a resume turn
// of their own, such as a parked program the opening input interrupted.
func (m *Service) SetWaitEnds(ends func(sessionID string) []string) { m.waitEnds = ends }

// deliverWaitEnds opens the turn with the waits that ended for it, in the
// form a resume turn delivers them.
func (m *Service) deliverWaitEnds(ctx context.Context, id string) error {
	if m.waitEnds == nil {
		return nil
	}
	for _, text := range m.waitEnds(id) {
		msg := api.Message{
			ID: uuid.NewString(), Role: api.MessageRoleUser, Content: text,
			Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted,
			Kind: api.MessageKindHostLoopWake, HostSignalID: string(anchor.LoopWake), CreatedAt: time.Now().UTC(),
		}
		transcript.StampUserVisibility(&msg)
		if err := m.transcript.Append(ctx, id, msg); err != nil {
			return err
		}
	}
	return nil
}
