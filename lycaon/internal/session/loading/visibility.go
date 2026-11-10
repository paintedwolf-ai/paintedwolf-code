package loading

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

// reconcileVisibility records changed continuation surfaces independently of decisions.
func (m *Service) reconcileVisibility(ctx context.Context, sess *api.Session, opening, surfaceID string, floor, loadable []string, failure turnload.RankingFailure) {
	if !m.Ledger.ReconcileCatalog(sess.ID, loadable, failure) {
		return
	}
	m.recordTurnLoad(ctx, sess, sess.ID, store.TurnLoadReceipt{
		Trigger:          store.TurnLoadTriggerVisibility,
		OpeningMessageID: opening,
		SurfaceID:        surfaceID,
		StateJSON:        marshalJSON(map[string]any{"failure": failure}),
		Decisions:        marshalJSON(map[string]any{"visibility": map[string]bool{"full_catalog": m.Ledger.FullCatalog(sess.ID)}, "floor": floor}),
		Abstained:        true,
		Reason:           string(failure),
	})
}
