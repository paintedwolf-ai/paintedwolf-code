package toolexecution

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/invocation"
)

const maxProgramFeedbackReferenceBytes = 512

// programOutcomeFacts carries ledger observations, never a verdict inferred
// from utility status or output. Owner-specific diagnostics remain separate.
func programOutcomeFacts(p invocation.ProgramProjection) (map[string]any, error) {
	if p.Program.ID == "" || len(p.Program.ID) > maxProgramFeedbackReferenceBytes || p.CompletedEffects < 0 || p.UncertainEffects < 0 || p.ExecutedChildren < 0 || p.ExecutedReads < 0 || p.ExecutedReads > p.ExecutedChildren || p.StaticNotStarted < 0 || p.StaticUntaken < 0 || p.OmittedEffects < 0 || p.OmittedUnknown < 0 || p.OmittedEffects > p.CompletedEffects || p.OmittedUnknown > p.UncertainEffects {
		return nil, fmt.Errorf("invalid program effect projection")
	}
	if p.CompletedEffects != len(p.Effects)+p.OmittedEffects || p.UncertainEffects != len(p.Unknown)+p.OmittedUnknown {
		return nil, fmt.Errorf("program effect detail counts contradict aggregate")
	}
	effects, omittedEffects, err := programEffectDetails(p.Program.ID, p.Effects)
	if err != nil {
		return nil, err
	}
	unknown, omittedUnknown, err := programEffectDetails(p.Program.ID, p.Unknown)
	if err != nil {
		return nil, err
	}
	// References are complete identities. An oversized one is omitted whole,
	// never shortened into another executable recovery or evidence identity.
	detail, detailOmitted := programFeedbackReference(p.DetailReference)
	progress, progressOmitted := programFeedbackReference(p.Program.ProgressRef)
	facts := map[string]any{
		"program_id":                                 p.Program.ID,
		"program_completed_effects":                  p.CompletedEffects,
		"program_uncertain_effects":                  p.UncertainEffects,
		"program_executed_children":                  p.ExecutedChildren,
		"program_executed_reads":                     p.ExecutedReads,
		"program_static_not_started":                 p.StaticNotStarted,
		"program_static_untaken":                     p.StaticUntaken,
		"program_completed_details":                  effects,
		"program_unknown_details":                    unknown,
		"program_completed_details_retained_omitted": p.OmittedEffects + omittedEffects,
		"program_unknown_details_retained_omitted":   p.OmittedUnknown + omittedUnknown,
		"program_effect_details_omitted":             p.OmittedEffects+omittedEffects > 0 || p.OmittedUnknown+omittedUnknown > 0 || detailOmitted,
		"program_detail_ref":                         detail,
		"program_progress_state":                     p.Program.ProgressState,
		"program_progress_ref":                       progress,
		"program_progress_ref_omitted":               progressOmitted,
		"program_replay_available":                   false,
	}
	if p.Stopped != nil {
		stopped, omitted, err := programEffectDetails(p.Program.ID, []invocation.ExecutionProjection{*p.Stopped})
		if err != nil {
			return nil, err
		}
		facts["program_stopped_details"] = stopped
		facts["program_stopped_details_omitted"] = omitted > 0
	}
	// The recorder proves eligibility from actual reached/start markers and exact
	// source retention. Defense in depth refuses contradictory compact inputs.
	if p.FreshInvocationReference != "" && (p.CompletedEffects != 0 || p.UncertainEffects != 0 || p.ExecutedChildren != 0 || p.OmittedEffects != 0 || p.OmittedUnknown != 0 || p.FreshInvocationLimitation != "" || p.Program.State != "rejected") {
		return nil, fmt.Errorf("replay reference contradicts program execution facts")
	}
	if p.FreshInvocationReference != "" {
		reference, omitted := programFeedbackReference(p.FreshInvocationReference)
		facts["program_replay_available"] = !omitted
		if !omitted {
			facts["program_replay_source_ref"] = reference
		}
	} else {
		reason, omitted := programFeedbackReference(p.FreshInvocationLimitation)
		if !omitted {
			facts["program_replay_limitation"] = reason
		}
	}
	return facts, nil
}

func programFeedbackReference(value string) (string, bool) {
	if len(value) > maxProgramFeedbackReferenceBytes {
		return "", true
	}
	return strings.Clone(value), false
}
