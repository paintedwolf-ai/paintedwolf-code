package toolexecution

import (
	"github.com/lycaon/lycaon/internal/toolapproval"

	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"strings"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/pkg/api"
)

type toolApprovalRaise struct {
	Action          hitl.ProposedAction
	Plan            *hitl.ApprovalPlan
	Title           string
	ToolCallID      string
	ProjectID       string
	ApprovalMatches []hitl.ApprovalRuleMatch
	Explanation     *hitl.ApprovalExplanation
	Decision        *gate.Decision

	Detection              *hitl.DetectionMatch
	DetectionEndpoints     []authzledger.CapabilityEndpoint
	SecretScreenHit        bool
	AIRationalePending     bool
	AttachRationale        bool
	Rationale              toolapproval.AIRationaleAttachRequest
	GrantOffers            []hitl.ApprovalGrantOffer
	GrantDelta             string
	CoalesceKey            string
	SocketCapability       *hitl.SocketCapability
	DirectIPCapability     *hitl.DirectIPCapability
	DeclaredEndpoints      *hitl.DeclaredEndpoints
	SecretScreen           *hitl.SecretScreen
	ConsequenceBand        api.ConsequenceBand
	ConsequenceCode        api.ConsequenceCode
	SkipGrantOfferAutofill bool
	// Presence shows the call's pending mutations as held while the checkpoint waits.
	Presence tools.PresenceReporter
}

// releasesHeld reports whether the card would hand over values a person stored.
func (in toolApprovalRaise) releasesHeld() bool {
	return in.held() != nil
}

func (in toolApprovalRaise) held() *hitl.HeldRelease {
	if in.Plan != nil && in.Plan.Held != nil {
		return in.Plan.Held
	}
	if in.SecretScreen != nil {
		return in.SecretScreen.Held
	}
	return nil
}

func (e *Approvals) grantOffers(action hitl.ProposedAction, result *hitl.ApprovalResult) []hitl.ApprovalGrantOffer {
	if e == nil || e.approvalGate == nil {
		return nil
	}
	return e.approvalGate.GrantOffers(action, result)
}

// absorbedGrantOffers omits duration choices supplied by the enclosing capability card.
func (e *Approvals) absorbedGrantOffers(action hitl.ProposedAction, result *hitl.ApprovalResult) []hitl.ApprovalGrantOffer {
	if e == nil || e.approvalGate == nil {
		return nil
	}
	return e.approvalGate.AbsorbedGrantOffers(action, result)
}

func (e *Approvals) consequenceForToolApproval(in toolApprovalRaise) (api.ConsequenceBand, api.ConsequenceCode) {
	if in.ConsequenceBand != "" || in.ConsequenceCode != "" {
		return in.ConsequenceBand, in.ConsequenceCode
	}
	if e == nil || e.consequence == nil {
		return "", ""
	}
	level := ""
	if in.Detection != nil {
		level = in.Detection.Level
	}
	return e.consequence.ToolApproval(level, in.SecretScreenHit)
}

func (e *Approvals) raiseAndWaitToolApproval(ctx context.Context, in toolApprovalRaise) (*hitl.CheckpointResponse, error) {
	if e == nil || e.checkpointMgr == nil {
		return nil, fmt.Errorf("checkpoints not configured")
	}
	if final, ok := hitl.PreparedApprovalAnswer(ctx, &in.Action, in.Plan); ok {
		return final, nil
	}
	if hitl.HasPreparedApprovalAnswers(ctx) && (in.SocketCapability != nil || in.DirectIPCapability != nil) {
		return nil, &toolrejection.ToolReject{Code: isolation.CodeApprovalUnavailable, Data: map[string]any{"reason": "reviewed capabilities changed before execution"}}
	}
	actionKey := hitl.GrantKey(in.Action)
	if actionKey == "" {
		return nil, fmt.Errorf("approval action identity cannot be encoded")
	}
	grantKey := strings.TrimSpace(in.CoalesceKey)
	if grantKey == "" {
		grantKey = actionKey
	}
	chat := in.Action.ChatSession()
	toolCallID := strings.TrimSpace(in.ToolCallID)

	// A quiet never answers for a person who must confirm a held release.
	if !in.releasesHeld() && e.decisionQuieted(chat, in) {
		e.noteQuietSuppressed(chat, in)
		e.noteRepeatSuppressed(chat, in)
		e.recordAskSuppressed(ctx, in, authzledger.AskSuppressedCauseQuiet)
		return &hitl.CheckpointResponse{
			Status: hitl.DecisionStatusApproved,
			Result: &hitl.DecisionResult{Approved: true},
		}, nil
	}

	if e.approvalCoalesce != nil {
		// A card that settled between Begin and the join answered only the
		// actions it held; the joiner drops the stale entry and reviews its own.
		for attempt := 0; attempt < 2; attempt++ {
			begin, existingID := e.approvalCoalesce.Begin(chat, grantKey)
			switch begin {
			case toolapproval.ToolApprovalCoalesceSkipDenied:
				e.noteRepeatSuppressed(chat, in)
				e.recordAskSuppressed(ctx, in, authzledger.AskSuppressedCausePriorDeny)
				// No checkpoint exists for a prior-deny suppression; the original card's
				// resolution already sealed its detection_resolved row in-tx.
				return &hitl.CheckpointResponse{Status: hitl.DecisionStatusRejected}, nil
			case toolapproval.ToolApprovalCoalesceJoin:
				final, err := e.joinOpenCard(ctx, in, chat, grantKey, toolCallID, existingID)
				if !errors.Is(err, hitl.ErrCheckpointNotPending) {
					return final, err
				}
				e.approvalCoalesce.ClearPending(chat, grantKey)
				continue
			case toolapproval.ToolApprovalCoalesceMint:
			}
			break
		}
	}

	band, code := e.consequenceForToolApproval(in)
	if len(in.GrantOffers) == 0 && !in.SkipGrantOfferAutofill && e.approvalGate != nil {
		result := &hitl.ApprovalResult{Decision: in.Decision}
		in.GrantOffers = e.approvalGate.GrantOffers(in.Action, result)
	}
	joinedIDs := []string{}
	if toolCallID != "" {
		joinedIDs = []string{toolCallID}
	}
	var repeat *hitl.RepeatContext
	if !hitl.ApprovalPreparing(ctx) {
		repeat = e.noteRepeatAsk(chat, in)
	}
	checkpoint := hitl.CheckpointRequest{
		SessionID:          in.Action.SessionID,
		Kind:               api.CheckpointKindToolApproval,
		Type:               hitl.DecisionTypeApprove,
		Title:              in.Title,
		ToolCallID:         toolCallID,
		ProjectID:          in.ProjectID,
		ProposedAction:     &in.Action,
		ApprovalPlan:       in.Plan,
		ApprovalMatches:    in.ApprovalMatches,
		Explanation:        in.Explanation,
		GrantOffers:        in.GrantOffers,
		GrantDelta:         in.GrantDelta,
		Decision:           in.Decision,
		Detection:          in.Detection,
		DetectionEndpoints: append([]authzledger.CapabilityEndpoint(nil), in.DetectionEndpoints...),
		AIRationalePending: in.AIRationalePending,
		JoinedCount:        1,
		Repeat:             repeat,
		JoinedToolCallIDs:  joinedIDs,
		CoalesceChat:       chat,
		CoalesceGrantKey:   grantKey,
		ConsequenceBand:    band,
		ConsequenceCode:    code,
		SocketCapability:   in.SocketCapability,
		DirectIPCapability: in.DirectIPCapability,
		DeclaredEndpoints:  in.DeclaredEndpoints,
		SecretScreen:       in.SecretScreen,
		QuietSkipKeys:      e.quietSkipKeys(chat, in),
	}
	if err := hitl.PrepareApproval(ctx, checkpoint, func(final *hitl.CheckpointResponse) {
		if final != nil && final.Status == hitl.DecisionStatusRejected && e.approvalCoalesce != nil {
			e.approvalCoalesce.RecordDeny(chat, grantKey)
		}
	}); err != nil {
		if e.approvalCoalesce != nil {
			e.approvalCoalesce.AbortMint(chat, grantKey)
		}
		return nil, err
	}
	resp, err := e.checkpointMgr.RequestCheckpoint(ctx, checkpoint)
	if err != nil {
		if e.approvalCoalesce != nil {
			e.approvalCoalesce.AbortMint(chat, grantKey)
		}
		return nil, err
	}
	if e.approvalCoalesce != nil {
		e.approvalCoalesce.RegisterPending(chat, grantKey, resp.CheckpointID, toolCallID)
	}
	if in.AttachRationale && e.aiRationale != nil {
		e.aiRationale.AttachAsync(ctx, toolapproval.AIRationaleAttachRequest{
			CheckpointID: resp.CheckpointID,
			ToolContext:  in.Rationale.ToolContext,
			Tool:         in.Rationale.Tool,
			Args:         in.Rationale.Args,
			Files:        in.Action.Files,
			Explanation:  in.Explanation,
		})
	}
	defer tools.HoldForApproval(in.Presence, resp.CheckpointID)()
	// A send waiting only on the unlock waits for this card instead of
	// raising its own, and a refusal here holds it too.
	if held := in.held(); held != nil && !held.UnlockOnly {
		closed := e.Secrets.heldAsks.open(held.ChatSessionID)
		final, err := hitl.WaitForCheckpoint(ctx, e.checkpointMgr, resp.CheckpointID)
		closed(err == nil && final != nil && final.Status == hitl.DecisionStatusRejected)
		return final, err
	}
	return hitl.WaitForCheckpoint(ctx, e.checkpointMgr, resp.CheckpointID)
}

// joinOpenCard registers this call on the open card for its subject and waits
// for the answer; a card that already settled fails with ErrCheckpointNotPending.
func (e *Approvals) joinOpenCard(ctx context.Context, in toolApprovalRaise, chat, grantKey, toolCallID, checkpointID string) (*hitl.CheckpointResponse, error) {
	count := e.approvalCoalesce.NoteJoin(chat, grantKey, toolCallID)
	ids := e.approvalCoalesce.JoinedToolCallIDs(chat, grantKey)
	band, code := e.consequenceForToolApproval(in)
	if err := e.checkpointMgr.PatchPendingToolApprovalJoined(ctx, checkpointID, count, ids, string(band), string(code)); err != nil {
		return nil, err
	}
	e.recordAskSuppressed(ctx, in, authzledger.AskSuppressedCauseJoinedOpenCard)
	defer tools.HoldForApproval(in.Presence, checkpointID)()
	return hitl.WaitForCheckpoint(ctx, e.checkpointMgr, checkpointID)
}

func (e *Approvals) recordAskSuppressed(ctx context.Context, in toolApprovalRaise, cause string) {
	if e == nil || e.authzRecorder == nil {
		return
	}
	tool := strings.TrimSpace(in.Action.Tool)
	if tool == "" {
		tool = "command"
	}
	_ = e.authzRecorder.AppendCapabilityRecord(ctx, authzledger.CapabilityRecord{
		SessionID:        in.Action.SessionID,
		Action:           authzledger.ActionAskSuppressed,
		Outcome:          authzledger.OutcomeDenied,
		ResolvedBy:       authzledger.ResolvedBySystemDeny,
		Tool:             tool,
		SuppressionCause: cause,
		AskFamily:        authzledger.AskFamilyToolApproval,
		FailClosed:       false,
	})
}

func (e *Approvals) recordToolDenied(
	ctx context.Context,
	eval platform.PolicyContext,
	decision *platform.PolicyDecision,
	approval *hitl.ApprovalResult,
) {
	if e == nil || e.authzRecorder == nil {
		return
	}
	action := hitl.ProposedAction{
		Tool:               eval.ToolName,
		Args:               eval.ToolArgs,
		Files:              filesFromArgs(eval.ToolName, eval.ToolArgs),
		ResolvedFiles:      eval.ResolvedFiles,
		ProjectID:          eval.ProjectID,
		ProjectDir:         eval.ProjectDir,
		SessionID:          eval.SessionID,
		RootSessionID:      eval.ChatSessionID(),
		SessionScratchRoot: eval.ConfineRequest.SessionScratchRoot,
	}
	record := authzledger.ToolDeniedRecord{
		SessionID:       eval.SessionID,
		ParentSessionID: eval.ParentSessionID,
		Tool:            eval.ToolName,
		Args:            eval.ToolArgs,
		Files:           action.Files,
		ProjectDir:      eval.ProjectDir,
	}
	if decision != nil {
		record.BlockReason = decision.BlockReason
		record.RejectCode = decision.RejectCode
	}
	if approval != nil {
		for _, rule := range approval.MatchedRules {
			record.ApprovalRules = append(record.ApprovalRules, authzledger.ApprovalRuleCitation{
				Category: rule.Category, Pattern: rule.Pattern, Effect: rule.Effect,
				UnitID: rule.UnitID, PackID: rule.PackID, Scope: rule.Scope,
			})
		}
	}
	e.authzRecorder.AppendToolDenied(ctx, record)
}

func (e *Approvals) SetCheckpointManager(mgr hitl.CheckpointManager, gate hitl.ApprovalGate) {
	if e != nil {
		e.checkpointMgr = mgr
		e.approvalGate = gate
		confine.SetEgressResolver(e.Network.resolveEgress)
	}
}

func (e *Approvals) SetToolApprovalCoalesce(rt toolapproval.ToolApprovalCoalesce) {
	if e != nil {
		e.approvalCoalesce = rt
	}
}

func (e *Approvals) SetGateRepeatLedger(rt toolapproval.GateRepeatLedger) {
	if e != nil {
		e.gateRepeat = rt
	}
}

func (e *Approvals) SetConsequenceDeriver(d toolapproval.ConsequenceDeriver) {
	if e != nil {
		e.consequence = d
	}
}

func (e *Approvals) SetApprovalExplainer(explainer ApprovalExplainer) {
	if e != nil {
		e.approvalExplainer = explainer
	}
}

func (e *Approvals) SetBackgroundCommandResolver(resolver BackgroundCommandResolver) {
	if e != nil {
		e.backgroundCommand = resolver
	}
}

func (e *Approvals) SetAuthzRecorder(r authzledger.Recorder) {
	if e != nil {
		e.authzRecorder = r
	}
}

func (e *Approvals) SetApprovalOutcomeRenderer(r ApprovalOutcomeRenderer) {
	if e != nil {
		e.approvalOutcome = r
	}
}

func (e *Approvals) SetAIRationaleAttacher(a toolapproval.AIRationaleAttacher) {
	if e != nil {
		e.aiRationale = a
	}
}

func (e *Approvals) outcomeMessage(code string, context map[string]any) string {
	if e.approvalOutcome != nil {
		if msg := strings.TrimSpace(e.approvalOutcome.ApprovalOutcome(code, context)); msg != "" {
			return msg
		}
	}
	return code
}
