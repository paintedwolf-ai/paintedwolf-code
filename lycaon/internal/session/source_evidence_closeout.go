package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/verification"
	"github.com/lycaon/lycaon/pkg/api"
)

const sourceEvidenceUnmetBeforeCloseoutCode = "SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT"

// maybeRejectCloseoutForSourceEvidence enforces only explicit workflow checks.
func (m *Manager) maybeRejectCloseoutForSourceEvidence(
	ctx context.Context,
	sess *api.Session,
	history []api.Message,
	surfaceID string,
	invokeAllowed bool,
) (*guidance.Refusal, bool) {
	if m == nil || sess == nil || !invokeAllowed {
		return nil, false
	}
	if sess.IsWorkerChild() {
		return nil, false
	}
	surfaceID = strings.TrimSpace(surfaceID)
	if surfaceID == spawn.SurfaceImplementSynthesis || !guard.SurfaceFinishesWithUserProse(surfaceID) {
		return nil, false
	}
	if !m.activePhaseRequiresVerify(ctx, sess.ID) {
		return nil, false
	}
	assessment := closeoutVerification(history)
	passed, _, exhausted := m.verifyGateState(ctx, sess, history)
	if passed || exhausted || assessment.Valid() && assessment.Method == verification.Blocked {
		return nil, false
	}
	return m.tryOARFinishBlock(ctx, sess, func(gc *oar.GuardContext) error {
		gc.VerifyRequired = true
		gc.VerifierPass = false
		revision, _ := m.verificationRevision(ctx, m.sourceEvidenceRoot(ctx, sess))
		gc.PutRejectData(sourceEvidenceUnmetBeforeCloseoutCode, map[string]any{
			"source_revision": revision,
			"cap":             maxVerifyAttemptsPerRun,
		})
		return nil
	})
}

func closeoutVerification(history []api.Message) *verification.Assessment {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role != api.MessageRoleAssistant || len(history[i].ToolCalls) != 0 {
			continue
		}
		report, ok := guidance.ParseCoordinatorCompletionReport(history[i].Content)
		if !ok {
			return nil
		}
		return report.Verification
	}
	return nil
}
