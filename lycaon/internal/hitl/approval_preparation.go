package hitl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/pkg/api"
)

type approvalPreparationKey struct{}
type approvalAnswersKey struct{}

// PreparedApproval holds a review pending invocation-wide approval.
type PreparedApproval struct {
	Request CheckpointRequest
	Settled func(*CheckpointResponse)
}

func (*PreparedApproval) Error() string { return "approval prepared for invocation review" }

func IsPreparedApproval(err error) bool {
	var prepared *PreparedApproval
	return errors.As(err, &prepared)
}

func WithApprovalPreparation(ctx context.Context) context.Context {
	return context.WithValue(ctx, approvalPreparationKey{}, true)
}

func ApprovalPreparing(ctx context.Context) bool {
	preparing, _ := ctx.Value(approvalPreparationKey{}).(bool)
	return preparing
}

// PrepareApproval returns a typed suspension only inside invocation preparation.
func PrepareApproval(ctx context.Context, req CheckpointRequest, settled func(*CheckpointResponse)) error {
	if !ApprovalPreparing(ctx) {
		return nil
	}
	if req.ApprovalPlan == nil || req.ProposedAction == nil || req.Decision == nil {
		return fmt.Errorf("capability preparation requires a complete approval plan")
	}
	if err := req.ApprovalPlan.Validate(); err != nil {
		return err
	}
	return &PreparedApproval{Request: req, Settled: settled}
}

// WithPreparedApprovalAnswer binds a decision to this invocation’s reviewed subjects.
func WithPreparedApprovalAnswer(ctx context.Context, prepared []*PreparedApproval, final *CheckpointResponse) context.Context {
	answers := map[string]*CheckpointResponse{}
	for _, review := range prepared {
		if key := preparedApprovalKey(review.Request.ProposedAction, review.Request.ApprovalPlan); key != "" {
			answers[key] = final
		}
		if review.Settled != nil {
			review.Settled(final)
		}
	}
	return context.WithValue(ctx, approvalAnswersKey{}, answers)
}

func HasPreparedApprovalAnswers(ctx context.Context) bool {
	return ctx.Value(approvalAnswersKey{}) != nil
}

func PreparedApprovalAnswer(ctx context.Context, action *ProposedAction, plan *ApprovalPlan) (*CheckpointResponse, bool) {
	answers, _ := ctx.Value(approvalAnswersKey{}).(map[string]*CheckpointResponse)
	answer, ok := answers[preparedApprovalKey(action, plan)]
	return answer, ok
}

func preparedApprovalKey(action *ProposedAction, plan *ApprovalPlan) string {
	if action == nil || plan == nil {
		return ""
	}
	key := CapabilityApprovalKey(*action, plan)
	if key == "" {
		return ""
	}
	raw, err := json.Marshal(struct {
		Action, Session, Call string
		Subject               ApprovalSubject
		Reasons               []api.ApprovalGate
	}{key, action.SessionID, action.ActionID, plan.Subject, plan.Reasons})
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// CapabilityApprovalKey separates different held subsets of one invocation.
func CapabilityApprovalKey(action ProposedAction, plan *ApprovalPlan) string {
	if plan == nil || GrantKey(action) == "" {
		return ""
	}
	raw, err := json.Marshal(struct {
		Action  string
		Subject ApprovalSubject
	}{GrantKey(action), plan.Subject})
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return "capabilities_" + hex.EncodeToString(digest[:])
}
