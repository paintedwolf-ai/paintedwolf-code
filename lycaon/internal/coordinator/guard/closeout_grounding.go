package guard

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

// CloseoutGroundingVerdict records citation observations before policy evaluation.
type CloseoutGroundingVerdict struct {
	Code                 string
	CitationsRequired    bool
	UnobservedHandles    []string
	UnobservedURLs       []string
	CitationUnverifiable bool
	Offenders            []string
	HintData             map[string]any
	Grounding            *api.CitationGrounding
}

// EvaluateCoordinatorCloseoutGrounding validates typed closeout citations before commit.
func EvaluateCoordinatorCloseoutGrounding(
	ctx context.Context,
	ledger guidance.CloseoutEvidenceReader,
	sess *api.Session,
	history []api.Message,
	surfaceID string,
	report guidance.CoordinatorCompletionReport,
	turnTools []string,
	roots evidence.CitationRoots,
) (CloseoutGroundingVerdict, error) {
	if sess == nil || sess.IsWorkerChild() {
		return CloseoutGroundingVerdict{}, nil
	}
	if !surface.SurfaceDeliversReport(surfaceID) {
		return CloseoutGroundingVerdict{}, nil
	}
	if strings.TrimSpace(roots.ProjectDir) == "" && len(roots.Roots) == 0 {
		return CloseoutGroundingVerdict{}, nil
	}
	report.Normalize()
	if strings.TrimSpace(report.Synthesis) == "" {
		return CloseoutGroundingVerdict{}, nil
	}
	if CoordinatorEvidenceOptional(history, turnTools) {
		return CloseoutGroundingVerdict{
			Grounding: &api.CitationGrounding{Traced: true},
		}, nil
	}
	if ledger == nil {
		return CloseoutGroundingVerdict{}, nil
	}

	ev, err := guidance.UnionCloseoutEvidence(ctx, ledger, sess.ID, history)
	if err != nil {
		return CloseoutGroundingVerdict{}, err
	}
	if guidance.TypedCitationChannelsEmpty(len(report.CitedEvidence), len(report.CitedURLs)) &&
		guidance.CloseoutLedgerHasCitableEvidence(ev) {
		// Recorded sources permit host attachment when citations are omitted.
		code := guidance.CloseoutCitationsRequiredCode(surfaceID)
		return CloseoutGroundingVerdict{
			Code:              code,
			CitationsRequired: true,
			HintData:          guidance.CloseoutRepairHintData(roots, surfaceID, report, ev, nil),
		}, nil
	}
	eval := guidance.EvaluateCloseoutCitations(roots, surfaceID, report, ev)
	grounding := guidance.BuildCloseoutCitationGrounding(roots, surfaceID, report, ev, eval)
	verdict := closeoutVerdictFromEval(eval, ev.Ledger, grounding)
	if verdict.Code != "" {
		verdict.HintData = guidance.CloseoutRepairHintData(roots, surfaceID, report, ev, verdict.Offenders)
	}
	return verdict, nil
}

func closeoutVerdictFromEval(eval guidance.CloseoutGroundingEval, ev evidence.Ledger, grounding *api.CitationGrounding) CloseoutGroundingVerdict {
	code := strings.TrimSpace(eval.Code)
	offenders := append([]string(nil), eval.Offenders...)
	if code == "" && len(offenders) == 0 && !eval.CitationUnverifiable {
		if grounding != nil {
			grounding.Traced = true
		}
		return CloseoutGroundingVerdict{Grounding: grounding}
	}
	hintData := guidance.GroundingHintData(offenders, ev)
	if grounding != nil {
		grounding.Traced = false
		grounding.HintCode = code
	}
	v := CloseoutGroundingVerdict{
		Code:                 code,
		UnobservedHandles:    append([]string(nil), eval.UnobservedHandles...),
		UnobservedURLs:       append([]string(nil), eval.UnobservedURLs...),
		CitationUnverifiable: eval.CitationUnverifiable,
		Offenders:            offenders,
		HintData:             hintData,
		Grounding:            grounding,
	}
	return v
}

// maxKickDraftedSynthesisChars bounds synthesis retained for citation retries.
const maxKickDraftedSynthesisChars = 6000

// FormatCloseoutGroundingReject renders a citation retry.
func FormatCloseoutGroundingReject(
	ctx context.Context,
	rejectFmt *guidance.StaticRejectFormatter,
	renderHostKick HostKickRenderer,
	kickID string,
	attempt, maxAttempts int,
	decision *oar.Decision,
	draftedSynthesis string,
) (string, error) {
	var reject string
	var err error
	if decision.Copy != nil {
		reject, err = guidance.RenderPolicyCopy(ctx, decision.Code, string(oar.EffectBlock), decision.Copy)
	} else {
		if rejectFmt == nil {
			return "", fmt.Errorf("reject formatter required")
		}
		reject, err = rejectFmt.Format(decision.Code, decision.Data)
	}
	data := decision.Data
	if err != nil {
		return "", err
	}
	templateData := make(map[string]any, len(data)+4)
	for key, value := range data {
		templateData[key] = value
	}
	templateData["attempt"] = attempt
	templateData["max_attempts"] = maxAttempts
	templateData["drafted_synthesis"] = boundedDraftedSynthesis(draftedSynthesis)
	templateData["pinned_body_chars"] = len(strings.TrimSpace(draftedSynthesis))
	header := renderHostKickHeader(renderHostKick, kickID, templateData)
	if header == "" {
		return reject, nil
	}
	return header + "\n\n" + reject, nil
}

// boundedDraftedSynthesis trims and length-bounds the rejected synthesis for the kick.
func boundedDraftedSynthesis(synthesis string) string {
	s := strings.TrimSpace(synthesis)
	if len(s) <= maxKickDraftedSynthesisChars {
		return s
	}
	// The cap is a byte offset, so it can land mid-rune.
	return strings.TrimSpace(strings.ToValidUTF8(s[:maxKickDraftedSynthesisChars], "")) + "\n…"
}

// HostKickRenderer renders host kick templates from config/packs/painted-wolf/platform/guidance/.
type HostKickRenderer func(kickID string, data map[string]any) string

func renderHostKickHeader(render HostKickRenderer, kickID string, data map[string]any) string {
	if render == nil {
		return HostKickMarker(kickID)
	}
	return strings.TrimSpace(render(kickID, data))
}

// HostKickMarker returns the marker for a valid host kick ID.
func HostKickMarker(kickID string) string {
	kickID = strings.TrimSpace(kickID)
	if kickID == "" {
		return ""
	}
	if _, ok := anchor.ParseID(kickID); ok {
		return "[host:" + kickID + "]"
	}
	return ""
}
