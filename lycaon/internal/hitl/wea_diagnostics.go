package hitl

import (
	"strings"

	"github.com/lycaon/lycaon/internal/observability/cmddiag"
)

// observeApprovalResolutionWEA emits ONLY the real owning approval plan and
// installed grant facts after commitApprovalOperation succeeds. It uses the
// plan's real ActionDigest and shared monotonic sequence so independently
// settled execution effects from toolexecution can join against it in
// cmddiag.Observer without fabricating companion effects.
// Zero overhead when diagnostics are disabled.
func observeApprovalResolutionWEA(sessionID, checkpointID string, plan *ApprovalPlan, option ApprovalOption, result *DecisionResult) bool {
	if !cmddiag.Enabled() || result == nil {
		return false
	}
	seq := cmddiag.NextWEASeq()
	sess := strings.TrimSpace(sessionID)
	cpID := strings.TrimSpace(checkpointID)
	digest := ""
	if plan != nil {
		digest = strings.TrimSpace(plan.ActionDigest)
	}
	if digest == "" {
		digest = cmddiag.DigestReference("approval_plan", cpID, option.ID)
	}

	planFact := cmddiag.WEAApprovalPlanFact{
		ApprovalPlanID: cpID,
		SessionID:      sess,
		ActionKind:     string(option.Kind),
		ActionDigest:   digest,
		DecisionSeq:    seq,
		Approved:       result.Approved,
		Refused:        !result.Approved,
		Settled:        true,
		SourceRef:      cmddiag.DigestReference("checkpoint", cpID),
	}

	limit := min(len(option.Authority), cmddiag.DefaultMaxIDsPerFinding)
	grants := make([]cmddiag.WEAGrantFact, 0, limit)
	for i := 0; i < limit; i++ {
		delta := option.Authority[i]
		if delta.Grant == nil {
			continue
		}
		g := delta.Grant
		scope := strings.TrimSpace(string(g.Scope))
		if scope == "" {
			scope = "exact_action"
		}
		grantSess := strings.TrimSpace(g.ChatSessionID)
		if grantSess == "" && (scope == "chat" || scope == "exact_action") {
			grantSess = sess
		}
		grants = append(grants, cmddiag.WEAGrantFact{
			GrantID:      strings.TrimSpace(g.ID),
			SessionID:    grantSess,
			ProjectID:    strings.TrimSpace(g.ProjectID),
			Scope:        scope,
			ActionKind:   strings.TrimSpace(g.Predicate.Category),
			ActionDigest: digest,
			GrantedSeq:   seq,
			Active:       true,
			Settled:      true,
			SourceRef:    cmddiag.DigestReference("approval_grant", g.ID),
		})
	}

	return cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID:     sess,
		ScopeKey:      cpID,
		Settled:       true,
		ApprovalPlans: []cmddiag.WEAApprovalPlanFact{planFact},
		Grants:        grants,
	})
}
