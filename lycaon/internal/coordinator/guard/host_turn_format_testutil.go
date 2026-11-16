package guard

import (
	"context"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

var (
	hostFinishPipelineOnce sync.Once
	hostFinishPipeline     *oar.GuardPipeline
)

func moduleConfigRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

// FormatHostNoToolTurnReject runs ObserveCoordinatorHostNoToolTurn then EvaluateBlock
// (post_turn then pre_invoke) and formats the first Decision. Used by unit tests that
// pin observation parity without a session Manager.
func FormatHostNoToolTurnReject(
	sess *api.Session,
	history []api.Message,
	lastAssistant string,
	turnTools []string,
	surfaceID string,
	workersIdle bool,
	implState surface.ImplementSessionState,
	rejectFmt *guidance.StaticRejectFormatter,
	batchTurn BatchTurnGuard,
) (string, bool) {
	gc := oar.NewGuardContext()
	var draft guidance.CloseoutDraft
	if surface.SurfaceDeliversReport(surfaceID) {
		draft = guidance.ParseCloseoutDraft(lastAssistant, citationmd.Options{})
	}
	ObserveCoordinatorHostNoToolTurn(
		sess, history, lastAssistant, draft, turnTools, surfaceID, workersIdle, implState, batchTurn, gc,
	)
	return formatFirstOARBlock(gc, rejectFmt, oar.AnchorCoordinatorCloseoutCheck, oar.AnchorCoordinatorPreInvoke)
}

// EvaluateObserveHasCode reports whether EvaluateBlock fires code for anchor (test helper).
func EvaluateObserveHasCode(gc *oar.GuardContext, anchor, code string) bool {
	if gc == nil || code == "" {
		return false
	}
	p := hostFinishOARPipeline()
	if p == nil {
		return false
	}
	res, err := p.EvaluateBlock(context.Background(), anchor, gc)
	if err != nil || res == nil {
		return false
	}
	return resultHasCode(res, code)
}
