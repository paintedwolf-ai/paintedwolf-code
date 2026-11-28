package toolexecution

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/observability/cmddiag"
	"github.com/lycaon/lycaon/internal/tools"
)

type authorityWitnessKey struct{}

// authorityExecutionWitness captures the authority requirement and any
// authorized checkpoint/grant binding during pre-invoke evaluation. It emits
// NOTHING during approval; observePostExecutionAuthorityEffectWEA emits the
// settled AuthorityEffect fact ONLY after registry.Run completes with err==nil.
type authorityExecutionWitness struct {
	requiresAuthority bool
	tool              string
	actionDigest      string
	approvalPlanID    string
	grantID           string
}

func withAuthorityExecutionWitness(ctx context.Context) context.Context {
	if !cmddiag.Enabled() {
		return ctx
	}
	return context.WithValue(ctx, authorityWitnessKey{}, &authorityExecutionWitness{})
}

func authorityWitnessFrom(ctx context.Context) *authorityExecutionWitness {
	if ctx == nil {
		return nil
	}
	w, _ := ctx.Value(authorityWitnessKey{}).(*authorityExecutionWitness)
	return w
}

func recordAuthorityRequirement(ctx context.Context, tool, actionDigest, grantID string) {
	w := authorityWitnessFrom(ctx)
	if w == nil {
		return
	}
	w.requiresAuthority = true
	if strings.TrimSpace(tool) != "" {
		w.tool = strings.TrimSpace(tool)
	}
	if strings.TrimSpace(actionDigest) != "" {
		w.actionDigest = strings.TrimSpace(actionDigest)
	}
	if strings.TrimSpace(grantID) != "" {
		w.grantID = strings.TrimSpace(grantID)
	}
}

func recordAuthorizedCheckpoint(ctx context.Context, action hitl.ProposedAction, final *hitl.CheckpointResponse) {
	w := authorityWitnessFrom(ctx)
	if w == nil || final == nil {
		return
	}
	w.requiresAuthority = true
	w.tool = strings.TrimSpace(action.Invocation.Tool)
	w.actionDigest = strings.TrimSpace(hitl.GrantKey(action))
	w.approvalPlanID = strings.TrimSpace(final.CheckpointID)
	if final.Result != nil && len(final.Result.GrantIDs) > 0 {
		w.grantID = strings.TrimSpace(final.Result.GrantIDs[0])
	}
}

// observePostExecutionAuthorityEffectWEA emits the settled authority-gated
// execution effect fact STRICTLY AFTER registry.Run succeeds (err == nil).
// An approval whose execution later fails or is canceled never reaches this
// seam and therefore never produces an executed-effect diagnostic fact.
// A real committed effect on a path that bypasses awaitActionApproval (leaving
// approvalPlanID and grantID empty while requiresAuthority is true) IS emitted
// and caught as unauthorized.
// Zero overhead when diagnostics are disabled.
func observePostExecutionAuthorityEffectWEA(ctx context.Context, tc tools.ToolContext) bool {
	if !cmddiag.Enabled() {
		return false
	}
	w := authorityWitnessFrom(ctx)
	if w == nil || !w.requiresAuthority {
		return false
	}
	sess := strings.TrimSpace(tc.Identity.SessionID)
	inv := strings.TrimSpace(tc.EffectActionID())
	if inv == "" {
		inv = strings.TrimSpace(tc.Identity.ToolCallID)
	}
	seq := cmddiag.NextWEASeq()
	effectID := inv
	if effectID == "" {
		effectID = cmddiag.DigestReference("auth_effect", sess, w.approvalPlanID, w.actionDigest)
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
				ActionKind:        w.tool,
				TargetRef:         cmddiag.DigestReference("tool_action", w.tool, w.actionDigest),
				ActionDigest:      w.actionDigest,
				ApprovalPlanID:    w.approvalPlanID,
				GrantID:           w.grantID,
				EventSeq:          seq,
				RequiresAuthority: true,
				Executed:          true,
				Settled:           true,
				SourceRef:         cmddiag.DigestReference("toolexecution", effectID),
			},
		},
	})
}
