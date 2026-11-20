package session

import (
	"context"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

func isSharedTempRoot(path string) bool {
	root := confine.SharedTempLocation(path)
	return root != "" && confine.PathEqual(root, fspath.CanonicalPath(path))
}

// A covering ancestor grants shared temp just as a descendant grants part of it.
func overlapsSharedTemp(path string) bool {
	path = fspath.CanonicalPath(path)
	for _, location := range confine.SharedTempLocations() {
		if confine.PathAtOrUnder(path, location) || confine.PathAtOrUnder(location, path) {
			return true
		}
	}
	return false
}

// ReviewSharedTemp settles all eligible locations before returning the original result.
func (b *WriteRootCheckpointBroker) ReviewSharedTemp(ctx context.Context, in command.SandboxWriteRootAsk, roots []string, failed bool) guidance.SharedTempSettlement {
	result := guidance.SharedTempSettlement{}
	if b == nil || b.Runtime == nil {
		return result
	}
	_, chat := askSessionIDs(ctx, b.Store, in.SessionID, in.ParentSessionID)
	history, err := b.Runtime.SharedTempHistory(ctx, chat, b.TempHistory)
	if err != nil {
		if !failed {
			return result
		}
		return guidance.SharedTempSettlement{Refused: roots, Unanswered: roots}
	}
	if !failed {
		first := history.FirstReports(roots)
		return guidance.SharedTempSettlement{Refused: first, Tolerated: len(first) > 0}
	}
	end, err := history.BeginReview(ctx)
	if err != nil {
		return guidance.SharedTempSettlement{Refused: roots, Unanswered: roots}
	}
	defer end()
	var held []string
	var reviews sharedTempReviews
	for _, root := range roots {
		if !isSharedTempRoot(root) {
			continue
		}
		result.Refused = append(result.Refused, root)
		in.ProposedWriteRoot = root
		prior := len(reviews)
		verdict, err := b.authorizeWriteRoot(ctx, in, func(ctx context.Context, req sandboxAskRequest) (sandboxAskResolution, error) {
			if history.Declined[root] {
				return sandboxAskResolution{Answered: true, Denied: true}, nil
			}
			return reviews.prepare(ctx, req)
		})
		switch {
		case err != nil:
			result.Unanswered = append(result.Unanswered, root)
		case verdict.Authorized:
			result.Allowed = append(result.Allowed, root)
		case verdict.Denied:
			result.Declined = append(result.Declined, root)
		case len(reviews) > prior:
			held = append(held, root)
		default:
			result.Unanswered = append(result.Unanswered, root)
		}
	}
	if len(reviews) == 0 {
		return result
	}
	answer := b.reviewTempLocations(ctx, reviews, held, in)
	for _, root := range held {
		switch {
		case sessionOverlayCovering(b.Runtime, chat, root) != "":
			result.Allowed = append(result.Allowed, root)
		case answer != nil && answer.Status == hitl.DecisionStatusRejected && answer.ResolvedBy == authzledger.ResolvedByHuman:
			result.Declined = append(result.Declined, root)
			history.Declined[root] = true
		default:
			result.Unanswered = append(result.Unanswered, root)
		}
	}
	return result
}

type sharedTempReviews []*hitl.PreparedApproval

func (reviews *sharedTempReviews) prepare(ctx context.Context, req sandboxAskRequest) (sandboxAskResolution, error) {
	key := "sandbox:" + req.AskFamily + ":" + req.Key
	declined, err := req.Checkpoints.ApprovalReviewSuppressed(ctx, req.InvokingSessionID, key)
	if err != nil {
		return sandboxAskResolution{}, err
	}
	if declined {
		return sandboxAskResolution{Answered: true, Denied: true}, nil
	}
	card, err := req.BuildCard()
	if err == nil {
		*reviews = append(*reviews, &hitl.PreparedApproval{Request: req.checkpoint(card)})
	}
	return sandboxAskResolution{}, err
}

func (b *WriteRootCheckpointBroker) reviewTempLocations(ctx context.Context, reviews []*hitl.PreparedApproval, roots []string, in command.SandboxWriteRootAsk) *hitl.CheckpointResponse {
	req := reviews[0].Request
	if len(reviews) > 1 {
		action := *req.ProposedAction
		action.Invocation.ActionID = in.ToolCallID
		action.Invocation.Args = map[string]any{"proposed_write_roots": slices.Clone(roots)}
		action.Presentation.Command = sandboxAskCommandSummary(in.Command)
		plan, decision, err := hitl.ComposeCapabilityApprovals(action, reviews)
		if err != nil {
			return nil
		}
		subject := hitl.ApprovalSubject{Kind: hitl.ApprovalSubjectWriteRootSet, Title: "Allow writes to " + strings.Join(roots, ", "), Targets: plan.Subject.Targets}
		plan, err = hitl.NewApprovalPlan(action, plan.Stage, subject, plan.Presentation, plan.Reasons, plan.Options, hitl.FaceContext{})
		if err != nil {
			return nil
		}
		req.ProposedAction, req.ApprovalPlan, req.Decision = &action, plan, decision
		req.Title, req.ApprovalMatches = subject.Title, plan.Presentation.ApprovalRules
	}
	req.ReviewKeys = hitl.PreparedReviewKeys(reviews)
	response, err := b.Checkpoints.RequestCheckpoint(ctx, req)
	if err != nil || response == nil {
		return nil
	}
	if response.Status != hitl.DecisionStatusPending {
		return response
	}
	response, err = hitl.WaitForCheckpoint(ctx, b.Checkpoints, response.CheckpointID)
	if err != nil {
		return nil
	}
	return response
}
