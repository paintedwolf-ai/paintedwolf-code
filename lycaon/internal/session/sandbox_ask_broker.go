package session

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/pkg/api"
)

// sandboxAskCommandSummaryRunes bounds the command line echoed onto an approval
// card: long enough to recognize the call, short enough not to become the card.
const sandboxAskCommandSummaryRunes = 160

// sandboxAskCommandSummary is the command line as an approval card shows it.
func sandboxAskCommandSummary(command string) string {
	return runeclamp.Clamp(strings.TrimSpace(command), sandboxAskCommandSummaryRunes)
}

// sandboxAskCard is a checkpoint request assembled after admission.
type sandboxAskCard struct {
	Action   hitl.ProposedAction
	Title    string
	Plan     *hitl.ApprovalPlan
	Decision *gate.Decision
	// ApprovalMatches are saved-approval rules already covering this action.
	ApprovalMatches []hitl.ApprovalRuleMatch
}

// sandboxAskRequest is one ask in the vocabulary the lifecycle needs.
type sandboxAskRequest struct {
	Gate        approvalstate.AskGate
	Checkpoints hitl.CheckpointManager
	Authz       authzledger.Recorder

	InvokingSessionID string
	// Key is the capability's axis key — a normalized path, a port set, an axis name.
	Key        string
	ToolCallID string
	ProjectID  string
	// Tool and AskFamily attribute a suppressed ask in the authz ledger.
	Tool      string
	AskFamily string
	// CheckpointLabel prefixes the error when RequestCheckpoint fails.
	CheckpointLabel string

	BuildCard func() (sandboxAskCard, error)
}

// sandboxAskResolution is the capability-neutral answer to one ask.
type sandboxAskResolution struct {
	// Answered reports whether the request reached a terminal answer.
	Answered     bool
	Raised       bool
	Authorized   bool
	Denied       bool
	UserGuidance string
}

// awaitSandboxAsk runs one ask from guard to settled checkpoint.
func awaitSandboxAsk(ctx context.Context, req sandboxAskRequest) (sandboxAskResolution, error) {
	var out sandboxAskResolution
	if req.Gate == nil || req.Checkpoints == nil || req.BuildCard == nil {
		return out, nil
	}
	if hitl.HasPreparedApprovalAnswers(ctx) {
		card, err := req.BuildCard()
		if err != nil {
			return out, err
		}
		if final, ok := hitl.PreparedApprovalAnswer(ctx, &card.Action, card.Plan); ok {
			if card.Plan.Subject.Kind == hitl.ApprovalSubjectReadPathSet || card.Plan.Subject.Kind == hitl.ApprovalSubjectWriteRootSet {
				return out, &toolrejection.ToolReject{Code: isolation.CodeApprovalUnavailable, Data: map[string]any{"reason": "approval did not install authority"}}
			}
			return sandboxAskAnswer(final), nil
		}
		return out, &toolrejection.ToolReject{Code: isolation.CodeApprovalUnavailable, Data: map[string]any{"reason": "reviewed capabilities changed before execution"}}
	}

	begin, existing := req.Gate.Begin(req.InvokingSessionID, req.Key, req.ToolCallID)
	switch begin {
	case approvalstate.SandboxAskSkipDenied:
		recordSandboxAskSuppressed(ctx, req, authzledger.AskSuppressedCausePriorDeny)
		return sandboxAskResolution{Answered: true, Raised: true, Denied: true}, nil
	case approvalstate.SandboxAskSkipToolCall:
		recordSandboxAskSuppressed(ctx, req, authzledger.AskSuppressedCauseDuplicateToolCall)
		return out, nil
	case approvalstate.SandboxAskJoin:
		return settleSandboxAsk(ctx, req, existing, false)
	case approvalstate.SandboxAskMint:
	default:
		return out, nil
	}

	card, err := req.BuildCard()
	if err != nil {
		req.Gate.AbortMint(req.InvokingSessionID, req.ToolCallID)
		return out, err
	}
	checkpoint := hitl.CheckpointRequest{
		SessionID: req.InvokingSessionID, Kind: api.CheckpointKindToolApproval, Title: card.Title,
		ToolCallID: req.ToolCallID, ProjectID: req.ProjectID, Type: hitl.DecisionTypeApprove,
		ProposedAction: &card.Action, ApprovalPlan: card.Plan, Decision: card.Decision,
		ApprovalMatches: card.ApprovalMatches,
	}
	if err := hitl.PrepareApproval(ctx, checkpoint, func(final *hitl.CheckpointResponse) {
		if hitl.CheckpointAuthorizes(final) {
			req.Gate.ClearDenied(req.InvokingSessionID, req.Key)
		} else {
			req.Gate.RecordDenied(req.InvokingSessionID, req.Key)
		}
	}); err != nil {
		req.Gate.AbortMint(req.InvokingSessionID, req.ToolCallID)
		return out, err
	}
	resp, err := req.Checkpoints.RequestCheckpoint(ctx, checkpoint)
	if err != nil {
		req.Gate.AbortMint(req.InvokingSessionID, req.ToolCallID)
		return out, fmt.Errorf("%s checkpoint: %w", req.CheckpointLabel, err)
	}
	req.Gate.RegisterPending(req.InvokingSessionID, req.Key, resp.CheckpointID)
	return settleSandboxAsk(ctx, req, resp.CheckpointID, true)
}

// settleSandboxAsk clears the guard only for the minting request.
func settleSandboxAsk(ctx context.Context, req sandboxAskRequest, checkpointID string, minted bool) (sandboxAskResolution, error) {
	final, err := hitl.WaitForCheckpoint(ctx, req.Checkpoints, checkpointID)
	if err != nil {
		if minted {
			req.Gate.Finish(req.InvokingSessionID, req.Key, true)
		}
		return sandboxAskResolution{}, err
	}
	authorized := hitl.CheckpointAuthorizes(final)
	if minted {
		req.Gate.Finish(req.InvokingSessionID, req.Key, !authorized)
		if authorized {
			req.Gate.ClearDenied(req.InvokingSessionID, req.Key)
		}
	}
	return sandboxAskAnswer(final), nil
}

func sandboxAskAnswer(final *hitl.CheckpointResponse) sandboxAskResolution {
	authorized := hitl.CheckpointAuthorizes(final)
	return sandboxAskResolution{
		Answered: true, Raised: true, Authorized: authorized, Denied: !authorized,
		UserGuidance: resolvedUserGuidance(final),
	}
}

func recordSandboxAskSuppressed(ctx context.Context, req sandboxAskRequest, cause string) {
	if req.Authz == nil {
		return
	}
	_ = req.Authz.AppendCapabilityRecord(ctx, authzledger.CapabilityRecord{
		SessionID: req.InvokingSessionID, Action: authzledger.ActionAskSuppressed,
		Outcome: authzledger.OutcomeDenied, ResolvedBy: authzledger.ResolvedBySystemDeny,
		Tool: req.Tool, SuppressionCause: cause,
		AskFamily: req.AskFamily, FailClosed: false,
	})
}

// askSessionIDs returns the invoking session and its chat.
func askSessionIDs(ctx context.Context, store Store, sessionID, parentSessionID string) (string, string) {
	invokingSessionID := strings.TrimSpace(sessionID)
	if invokingSessionID == "" {
		invokingSessionID = strings.TrimSpace(parentSessionID)
	}
	if invokingSessionID == "" {
		return "", ""
	}
	rootSessionID := sessiontree.RootID(ctx, store, invokingSessionID)
	if rootSessionID == "" {
		rootSessionID = invokingSessionID
	}
	return invokingSessionID, rootSessionID
}
