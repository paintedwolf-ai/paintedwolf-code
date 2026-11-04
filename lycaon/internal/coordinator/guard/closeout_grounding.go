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
	Resolved             guidance.DraftCitationResolution
}

// EvaluateCoordinatorCloseoutGrounding validates every supplied reference, then
// decides whether citations were required. A ledger failure is an error, never an
// empty ledger. Grounding is non-nil for any draft with an answer.
func EvaluateCoordinatorCloseoutGrounding(
	ctx context.Context,
	ledger guidance.CloseoutEvidenceReader,
	sess *api.Session,
	history []api.Message,
	surfaceID string,
	draft guidance.CloseoutDraft,
	roots evidence.CitationRoots,
) (CloseoutGroundingVerdict, error) {
	if sess == nil || sess.IsWorkerChild() || !surface.SurfaceDeliversReport(surfaceID) || !draft.HasAnswer() {
		return CloseoutGroundingVerdict{}, nil
	}
	ev, err := guidance.UnionCloseoutEvidence(ctx, ledger, sess.ID, history)
	if err != nil {
		return CloseoutGroundingVerdict{}, err
	}
	resolved := guidance.ResolveDraftCitations(roots, surfaceID, draft, ev, guidance.SessionPresentableArtifactIDs(history))
	eval := guidance.EvaluateDraftCitations(draft, resolved)
	report := draft.Report()
	if eval.Code == "" && draft.CitationCount() == 0 {
		if !CoordinatorEvidenceOptional(history, ev) && guidance.CloseoutLedgerHasCitableEvidence(ev) {
			// Recorded sources permit host attachment when citations are omitted.
			return CloseoutGroundingVerdict{
				Code:              guidance.CloseoutCitationsRequiredCode(surfaceID),
				CitationsRequired: true,
				HintData:          guidance.CloseoutRepairHintData(roots, draft, resolved, ev, nil),
				Resolved:          resolved,
			}, nil
		}
		return CloseoutGroundingVerdict{
			Grounding: guidance.EvidenceOptionalGrounding(report),
			HintData:  guidance.CloseoutRepairHintData(roots, draft, resolved, ev, nil),
			Resolved:  resolved,
		}, nil
	}
	grounding := guidance.BuildCloseoutCitationGrounding(roots, surfaceID, report, ev, eval)
	verdict := closeoutVerdictFromEval(eval, ev.Ledger, grounding)
	verdict.Resolved = resolved
	verdict.HintData = guidance.CloseoutRepairHintData(roots, draft, resolved, ev, verdict.Offenders)
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
