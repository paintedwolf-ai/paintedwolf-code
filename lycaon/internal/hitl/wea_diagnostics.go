package hitl

import (
	"strings"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/observability/cmddiag"
)

var hitlWEADiagSeq atomic.Int64

// observeApprovalResolutionWEA emits a bounded WEA authority observation after
// commitApprovalOperation succeeds. Uses a monotonic sequence number so
// legitimate revalidation or later superseding approvals never collide with
// earlier refusals. Zero overhead when diagnostics are disabled.
func observeApprovalResolutionWEA(sessionID, checkpointID string, option ApprovalOption, result *DecisionResult) bool {
	if !cmddiag.Enabled() || result == nil {
		return false
	}
	seq := hitlWEADiagSeq.Add(1)
	sess := strings.TrimSpace(sessionID)
	cpID := strings.TrimSpace(checkpointID)
	digest := cmddiag.DigestReference("approval_plan", cpID, option.ID)

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
