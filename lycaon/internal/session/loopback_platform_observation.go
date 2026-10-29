package session

import (
	"context"
	"net/netip"

	"github.com/lycaon/lycaon/internal/lineage"
)

// ObserveSessionListener supplies current complete native holder evidence to
// mediated connection admission. The returned identity grants no authority;
// partial visibility retains ordinary approval rather than cached provenance.
func (p *DefaultLoopbackProvenance) ObserveSessionListener(ctx context.Context, sessionID string, endpoint netip.AddrPort) (string, bool, error) {
	if p == nil {
		return "", false, lineage.ErrObservationIncomplete
	}
	return lineage.ObserveSessionListener(ctx, sessionID, endpoint)
}
