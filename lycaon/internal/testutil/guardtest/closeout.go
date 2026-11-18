package guardtest

import (
	"testing"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/pkg/api"
)

func FormatHostNoToolTurnReject(
	t *testing.T,
	sess *api.Session,
	history []api.Message,
	lastAssistant string,
	turnTools []string,
	surfaceID string,
	workersIdle bool,
	implState surface.ImplementSessionState,
	rejectFmt *guidance.StaticRejectFormatter,
	batchTurn guard.BatchTurnGuard,
) (string, bool) {
	t.Helper()
	gc := oar.NewGuardContext()
	var draft guidance.CloseoutDraft
	if surface.SurfaceDeliversReport(surfaceID) {
		draft = guidance.ParseCloseoutDraft(lastAssistant, citationmd.Options{})
	}
	guard.ObserveCoordinatorHostNoToolTurn(
		sess, history, lastAssistant, draft, turnTools, surfaceID, workersIdle, implState, batchTurn, gc,
	)
	return oartest.FormatFirstBlock(t, gc, rejectFmt, oar.AnchorCoordinatorCloseoutCheck, oar.AnchorCoordinatorPreInvoke)
}
