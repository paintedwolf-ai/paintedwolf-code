package session

import (
	"context"
	"slices"

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

func sharedTempWriteHistoryKey(path string) string { return "write:" + path }
func sharedTempReadHistoryKey(path string) string  { return "read:" + path }

// ReviewSharedTemp settles observed read and write needs on one card before returning the original result.
func (b *WriteRootCheckpointBroker) ReviewSharedTemp(ctx context.Context, in command.SandboxWriteRootAsk, observed confine.SharedTempObservation, failed bool) guidance.SharedTempSettlement {
	all := observed.All()
	result := guidance.SharedTempSettlement{Reads: slices.Clone(observed.Reads), Writes: slices.Clone(observed.Writes)}
	if b == nil || b.Runtime == nil || len(all) == 0 {
		return result
	}
	_, chat := askSessionIDs(ctx, b.Store, in.SessionID, in.ParentSessionID)
	history, err := b.Runtime.SharedTempHistory(ctx, chat, b.TempHistory)
	if err != nil {
		if !failed {
			return result
		}
		result.Refused, result.Unanswered = all, all
		return result
	}
	if !failed {
		first := history.FirstReports(all)
		result.Refused = first
		result.Tolerated = len(first) > 0
		return result
	}
	end, err := history.BeginReview(ctx)
	if err != nil {
		result.Refused, result.Unanswered = all, all
		return result
	}
	defer end()

	type heldTarget struct {
		path  string
		write bool
	}
	var held []heldTarget
	var reviews sharedTempReviews
	appendUnique := func(dst *[]string, v string) {
		if !slices.Contains(*dst, v) {
			*dst = append(*dst, v)
		}
	}

	if b.ReadRuntime != nil {
		readAsk := command.SandboxReadPathAsk{
			SessionID: in.SessionID, ParentSessionID: in.ParentSessionID, ProjectID: in.ProjectID,
			ToolCallID: in.ToolCallID, ActionID: in.ActionID, ProjectDir: in.ProjectDir, ToolName: in.ToolName,
			Command: in.Command, ReadDenyPaths: in.ReadDenyPaths, SessionScratchRoot: in.SessionScratchRoot,
			SessionOutputRoot: in.SessionOutputRoot,
		}
		for _, path := range observed.Reads {
			if confine.SharedTempLocation(path) == "" {
				continue
			}
			appendUnique(&result.Refused, path)
			readAsk.ProposedReadPath = path
			prior := len(reviews)
			verdict, err := b.authorizeReadPath(ctx, readAsk, func(ctx context.Context, req sandboxAskRequest) (sandboxAskResolution, error) {
				if history.Declined[sharedTempReadHistoryKey(req.Key)] {
					return sandboxAskResolution{Answered: true, Denied: true}, nil
				}
				return reviews.prepare(ctx, req)
			})
			switch {
			case err != nil:
				appendUnique(&result.Unanswered, path)
			case verdict.Authorized:
				appendUnique(&result.Allowed, path)
			case verdict.Denied:
				appendUnique(&result.Declined, path)
			case len(reviews) > prior:
				held = append(held, heldTarget{path: path, write: false})
			default:
				appendUnique(&result.Unanswered, path)
			}
		}
	}

	for _, root := range observed.Writes {
		if confine.SharedTempLocation(root) == "" {
			continue
		}
		appendUnique(&result.Refused, root)
		in.ProposedWriteRoot = root
		prior := len(reviews)
		verdict, err := b.authorizeWriteRoot(ctx, in, func(ctx context.Context, req sandboxAskRequest) (sandboxAskResolution, error) {
			if history.Declined[sharedTempWriteHistoryKey(req.Key)] || history.Declined[req.Key] {
				return sandboxAskResolution{Answered: true, Denied: true}, nil
			}
			return reviews.prepare(ctx, req)
		})
		switch {
		case err != nil:
			appendUnique(&result.Unanswered, root)
		case verdict.Authorized:
			appendUnique(&result.Allowed, root)
		case verdict.Denied:
			appendUnique(&result.Declined, root)
		case len(reviews) > prior:
			held = append(held, heldTarget{path: root, write: true})
		default:
			appendUnique(&result.Unanswered, root)
		}
	}

	if len(reviews) == 0 {
		return result
	}
	answer := b.reviewTempLocations(ctx, reviews, in)
	humanDeclined := answer != nil && answer.Status == hitl.DecisionStatusRejected && answer.ResolvedBy == authzledger.ResolvedByHuman
	authorized := hitl.CheckpointAuthorizes(answer)
	for _, item := range held {
		var covered bool
		if item.write {
			covered = b.coveringWriteGrant(chat, in.ProjectID, item.path) != ""
		} else {
			covered = b.readGrantCovers(chat, in.ProjectID, item.path)
		}
		if !covered && authorized {
			// Ensure standalone broker fixtures without authority installer commit the primary option.
			if item.write {
				b.Runtime.GrantSessionWriteRoot(chat, item.path)
				covered = true
			} else if b.ReadRuntime != nil {
				b.ReadRuntime.GrantSessionWriteRoot(chat, item.path)
				covered = true
			}
		}
		switch {
		case covered:
			appendUnique(&result.Allowed, item.path)
		case humanDeclined:
			appendUnique(&result.Declined, item.path)
			if item.write {
				history.Declined[sharedTempWriteHistoryKey(item.path)] = true
				if isSharedTempRoot(item.path) {
					history.Declined[item.path] = true
				}
			} else {
				history.Declined[sharedTempReadHistoryKey(item.path)] = true
			}
		default:
			appendUnique(&result.Unanswered, item.path)
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

func (b *WriteRootCheckpointBroker) reviewTempLocations(ctx context.Context, reviews []*hitl.PreparedApproval, in command.SandboxWriteRootAsk) *hitl.CheckpointResponse {
	req := reviews[0].Request
	if len(reviews) > 1 {
		action := *req.ProposedAction
		action.Invocation.ActionID = in.ToolCallID
		for i := range reviews {
			reviews[i].Request.ToolCallID = in.ToolCallID
			if reviews[i].Request.ProposedAction != nil {
				copied := *reviews[i].Request.ProposedAction
				copied.Invocation.ActionID = in.ToolCallID
				reviews[i].Request.ProposedAction = &copied
			}
		}
		action.Invocation.Args = map[string]any{"command": sandboxAskCommandSummary(in.Command)}
		action.Presentation.Command = sandboxAskCommandSummary(in.Command)
		plan, decision, err := hitl.ComposeCapabilityApprovals(action, reviews)
		if err != nil {
			return nil
		}
		req.ProposedAction, req.ApprovalPlan, req.Decision = &action, plan, decision
		req.Title, req.ApprovalMatches = plan.Subject.Title, plan.Presentation.ApprovalRules
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
