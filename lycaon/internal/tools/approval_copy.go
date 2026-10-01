package tools

import (
	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/pkg/api"
)

// ApprovalExplanation is reviewed host copy for a tool approval ask.
type ApprovalExplanation struct {
	What      string
	Who       string
	IfWrong   string
	AllowLine string
}

// ApprovalExplainer renders reviewed explanation copy for proposed actions.
type ApprovalExplainer interface {
	ExplainApproval(action hitl.ProposedAction) ApprovalExplanation
}

// BackgroundCommandResolver returns host-recorded command detail for an opaque
// background handle. It is presentation-only and never changes approval args.
type BackgroundCommandResolver func(sessionID, handle string) string

// ApprovalOutcomeRenderer renders catalog copy for tool approvals that end without approval.
type ApprovalOutcomeRenderer interface {
	// ApprovalOutcome returns the rendered message, or the code when it is unknown.
	ApprovalOutcome(code string, ctx map[string]any) string
}

func (e *DefaultToolExecutor) approvalRefusal(code string) error {
	class := api.FailureClassHostRejection
	if code == approvaloutcome.CodeApprovalDenied {
		class = api.FailureClassPolicyRejection
	}
	return guidance.NewRefusal(code, e.outcomeMessage(code, nil)).WithCause(&ToolReject{Code: code, FailureClass: class, OwnerRef: "approval"})
}
