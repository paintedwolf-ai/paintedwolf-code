package turnguards

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	sessionverification "github.com/lycaon/lycaon/internal/session/verification"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/verification"
	"github.com/lycaon/lycaon/pkg/api"
)

const sourceEvidenceUnmetBeforeCloseoutCode = "SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT"

// SourceEvidence enforces only explicit workflow checks. assessment is the current
// draft's `[verify:]`; a blocked one is the honest way out.
func (m *Service) SourceEvidence(
	ctx context.Context,
	sess *api.Session,
	history []api.Message,
	assessment *verification.Assessment,
	surfaceID string,
	invokeAllowed bool,
) (*guidance.Refusal, bool) {
	if m == nil || m.Verification == nil || sess == nil || !invokeAllowed {
		return nil, false
	}
	if sess.IsWorkerChild() {
		return nil, false
	}
	surfaceID = strings.TrimSpace(surfaceID)
	if surfaceID == spawn.SurfaceImplementSynthesis || !guard.SurfaceFinishesWithUserProse(surfaceID) {
		return nil, false
	}
	if !m.Verification.RequiresPhaseVerify(ctx, sess.ID) {
		return nil, false
	}
	passed, _, exhausted := m.Verification.GateState(ctx, sess, history)
	if passed || exhausted || assessment.Valid() && assessment.Method == verification.Blocked {
		return nil, false
	}
	return m.ToolPolicy.FinishBlock(ctx, sess, func(gc *oar.GuardContext) error {
		gc.Progress.VerifyRequired = true
		gc.Progress.VerifierPass = false
		revision, _ := m.Verification.Revision(ctx, m.Verification.SourceEvidenceRoot(ctx, sess))
		gc.PutRejectData(sourceEvidenceUnmetBeforeCloseoutCode, map[string]any{
			"source_revision": revision,
			"cap":             sessionverification.MaxAttemptsPerRun,
		})
		return nil
	})
}
