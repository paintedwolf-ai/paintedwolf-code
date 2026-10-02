package hitl

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/pkg/api"
)

// ErrNoCommonApprovalDuration keeps incompatible reviews separate.
var ErrNoCommonApprovalDuration = errors.New("capabilities have no common approval duration")

// ComposeCapabilityApprovals intersects durations and preserves each plan’s authority.
func ComposeCapabilityApprovals(action ProposedAction, reviews []*PreparedApproval) (*ApprovalPlan, *gate.Decision, error) {
	if len(reviews) == 0 {
		return nil, nil, fmt.Errorf("capability review has no plans")
	}
	decision := &gate.Decision{Posture: gate.PostureLight}
	subject := ApprovalSubject{Kind: ApprovalSubjectActionSet, Title: "Allow command capabilities"}
	presentation := ApprovalPresentation{Action: "Use the listed capabilities", Tool: action.Tool, Command: action.Command}
	var reasons []api.ApprovalGate
	var reasonKeys []string
	var held *HeldRelease
	plans := make([]*ApprovalPlan, 0, len(reviews))
	for _, review := range reviews {
		req := review.Request
		if req.ApprovalPlan == nil || req.Decision == nil || req.ProposedAction == nil || req.ProjectID != action.ProjectID ||
			req.SessionID != action.SessionID || req.ToolCallID != action.ActionID {
			return nil, nil, fmt.Errorf("capability review has incomplete or foreign contributions")
		}
		plan := req.ApprovalPlan
		if err := plan.Validate(); err != nil {
			return nil, nil, err
		}
		if plan.Stage != ApprovalStagePreSpawn {
			return nil, nil, fmt.Errorf("capability review crosses approval stages")
		}
		plans = append(plans, plan)
		held = held.merged(plan.Held)
		subject.Targets = append(subject.Targets, plan.Subject.Targets...)
		reasons = append(reasons, plan.Reasons...)
		decision.Posture = gate.Stricter(decision.Posture, req.Decision.Posture)
		decision.Cited = append(decision.Cited, req.Decision.Cited...)
		reasonKeys = append(reasonKeys, req.Decision.ReasonSegments()...)
		part := plan.Presentation
		band, code := MaxConsequence(api.ConsequenceBand(part.ConsequenceBand), api.ConsequenceCode(part.ConsequenceCode), req.ConsequenceBand, req.ConsequenceCode)
		part.ConsequenceBand, part.ConsequenceCode = string(band), string(code)
		mergeCapabilityPresentation(&presentation, part)
	}
	reasons = compactGates(reasons)
	if len(reasons) == 0 {
		return nil, nil, fmt.Errorf("capability review has no gate reasons")
	}
	decision.Primary, decision.Also = reasons[0], reasons[1:]
	decision.ReasonKey = strings.Join(uniqueCopy(reasonKeys), "|")
	presentation.Gate = decision.Primary
	options := combinedCapabilityOptions(plans)
	if !slices.ContainsFunc(options, func(option ApprovalOption) bool { return !option.Disabled }) {
		return nil, nil, ErrNoCommonApprovalDuration
	}
	options = append(options, combinedCapabilityQuiet(plans, options)...)
	plan, err := NewApprovalPlan(action, ApprovalStagePreSpawn, subject, presentation, reasons, options, FaceContext{})
	if err != nil {
		return nil, nil, err
	}
	plan, err = plan.RequirePresence(held)
	return plan, decision, err
}

// combinedCapabilityQuiet retains each plan's chat-scoped quiet authority.
func combinedCapabilityQuiet(plans []*ApprovalPlan, combined []ApprovalOption) []ApprovalOption {
	if quietGrantSource(combined) == nil {
		return nil
	}
	var quiet *ApprovalOption
	for _, plan := range plans {
		for _, option := range plan.Options {
			if option.Kind != ApprovalOptionQuiet || option.Disabled {
				continue
			}
			if quiet == nil {
				copy := option
				copy.ID, copy.Authority = "capabilities_quiet_chat", nil
				quiet = &copy
			} else {
				quiet.Coverage = joinCapabilityCopy(quiet.Coverage, option.Coverage)
			}
			for _, delta := range option.Authority {
				if delta.Kind == AuthorityAskQuiet {
					quiet.Authority = append(quiet.Authority, delta)
				}
			}
		}
	}
	if quiet == nil || len(quiet.Authority) == 0 {
		return nil
	}
	return []ApprovalOption{*quiet}
}

func mergeCapabilityPresentation(out *ApprovalPresentation, in ApprovalPresentation) {
	out.Impact = joinCapabilityCopy(out.Impact, in.Impact)
	out.Who = joinCapabilityCopy(out.Who, in.Who)
	out.IfWrong = joinCapabilityCopy(out.IfWrong, in.IfWrong)
	out.AllowLine = joinCapabilityCopy(out.AllowLine, in.AllowLine)
	out.Cited = append(out.Cited, in.Cited...)
	out.ApprovalRules = append(out.ApprovalRules, in.ApprovalRules...)
	if in.Detection != nil {
		out.Detection = in.Detection
	}
	if in.Location != nil {
		out.Location = in.Location
	}
	band, code := MaxConsequence(api.ConsequenceBand(out.ConsequenceBand), api.ConsequenceCode(out.ConsequenceCode), api.ConsequenceBand(in.ConsequenceBand), api.ConsequenceCode(in.ConsequenceCode))
	out.ConsequenceBand, out.ConsequenceCode = string(band), string(code)
}

func joinCapabilityCopy(a, b string) string {
	if b == "" || a == b {
		return a
	}
	return strings.TrimSpace(a + " " + b)
}

func uniqueCopy(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out
}

func combinedCapabilityOptions(plans []*ApprovalPlan) []ApprovalOption {
	var out []ApprovalOption
	for _, candidate := range plans[0].Options {
		if candidate.Group != "" || candidate.Kind == ApprovalOptionQuiet || candidate.DecisionAction != ApprovalOptionApprove {
			continue
		}
		option := candidate
		option.ID = "capabilities_" + string(option.Rung)
		option.Authority = append([]ApprovalAuthorityDelta(nil), candidate.Authority...)
		for _, plan := range plans[1:] {
			matching, ok := capabilityOptionAtRung(plan, candidate)
			if !ok {
				option.Disabled = true
				option.Note = "Choose a duration supported by every listed capability."
				continue
			}
			option.Authority = append(option.Authority, matching.Authority...)
			option.Coverage = joinCapabilityCopy(option.Coverage, matching.Coverage)
			option.ExpiresWhen = joinCapabilityCopy(option.ExpiresWhen, matching.ExpiresWhen)
			option.ReaskWhen = joinCapabilityCopy(option.ReaskWhen, matching.ReaskWhen)
		}
		out = append(out, option)
	}
	return out
}

func capabilityOptionAtRung(plan *ApprovalPlan, candidate ApprovalOption) (ApprovalOption, bool) {
	for _, option := range plan.Options {
		if !option.Disabled && option.Group == "" && option.Kind != ApprovalOptionQuiet &&
			option.DecisionAction == ApprovalOptionApprove && option.Rung == candidate.Rung && option.Scope == candidate.Scope {
			return option, true
		}
	}
	return ApprovalOption{}, false
}
