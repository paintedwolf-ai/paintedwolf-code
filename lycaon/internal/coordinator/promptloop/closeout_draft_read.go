package promptloop

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

// readCloseoutDraft is the turn's one read of a tool-free response on a report
// surface. It parses the storage-safe revision, the text model_outputs holds, so
// spans stay valid after screening. Every later gate takes the typed result.
func (l *turnCloseout) readCloseoutDraft(
	ctx context.Context,
	sess *api.Session,
	surfaceID string,
	st *promptLoopTurnState,
	history []api.Message,
	assistantMsg api.Message,
) (guidance.CloseoutRead, error) {
	if sess == nil || sess.IsWorkerChild() || !surface.SurfaceDeliversReport(surfaceID) {
		return guidance.CloseoutRead{}, nil
	}
	ev, err := guidance.UnionCloseoutEvidence(ctx, l.Deps.EvidenceLedger, sess.ID, history)
	if err != nil {
		return guidance.CloseoutRead{}, err
	}
	var retained guidance.RetainedCloseout
	if l.Deps.CloseoutStallState != nil {
		retained = l.Deps.CloseoutStallState(ctx, sess.ID)
	}
	stored, _ := l.Projection.storageSafeMessage(ctx, assistantMsg)
	opts := guidance.DraftParseOptions(l.Context.citationRoots(ctx, sess), ev.Ledger)
	return guidance.ReadCloseoutReport(stored.Content, retained, completionReportBinding(st).PhaseDeliversRunReport, opts), nil
}
