package toolexecution

import (
	"strings"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/observability/cmddiag"
	"github.com/lycaon/lycaon/internal/tools"
)

// observeSettledAuthorityEffectWEA emits ONLY the settled authority-gated
// execution effect fact using the owning ActionDigest (hitl.GrantKey) and
// checkpoint/grant references. ApprovalPlans and Grants are emitted
// independently by internal/hitl and joined inside cmddiag.Observer.
// Zero overhead when diagnostics are disabled.
func observeSettledAuthorityEffectWEA(action hitl.ProposedAction, tc tools.ToolContext, final *hitl.CheckpointResponse) bool {
	if !cmddiag.Enabled() || final == nil {
		return false
	}
	sess := strings.TrimSpace(tc.Identity.SessionID)
	if sess == "" {
		sess = strings.TrimSpace(action.Scope.SessionID)
	}
	inv := strings.TrimSpace(action.Invocation.ActionID)
	if inv == "" {
		inv = strings.TrimSpace(tc.Identity.ToolCallID)
	}
	digest := strings.TrimSpace(hitl.GrantKey(action))
	seq := cmddiag.NextWEASeq()
	effectID := inv
	if effectID == "" {
		effectID = cmddiag.DigestReference("auth_effect", sess, final.CheckpointID)
	}
	grantID := ""
	if final.Result != nil && len(final.Result.GrantIDs) > 0 {
		grantID = strings.TrimSpace(final.Result.GrantIDs[0])
	}
	return cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID:    sess,
		ProjectID:    strings.TrimSpace(tc.Identity.ProjectID),
		InvocationID: inv,
		ScopeKey:     effectID,
		Settled:      true,
		AuthorityEffects: []cmddiag.WEAAuthorityEffectFact{
			{
				EffectID:          effectID,
				SessionID:         sess,
				ProjectID:         strings.TrimSpace(tc.Identity.ProjectID),
				InvocationID:      inv,
				ActionKind:        strings.TrimSpace(action.Invocation.Tool),
				TargetRef:         cmddiag.DigestReference("tool_action", action.Invocation.Tool, digest),
				ActionDigest:      digest,
				ApprovalPlanID:    strings.TrimSpace(final.CheckpointID),
				GrantID:           grantID,
				EventSeq:          seq,
				RequiresAuthority: true,
				Executed:          true,
				Settled:           true,
				SourceRef:         cmddiag.DigestReference("toolexecution", effectID),
			},
		},
	})
}
