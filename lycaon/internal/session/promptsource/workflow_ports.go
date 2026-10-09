package promptsource

import (
	"context"

	"github.com/lycaon/lycaon/internal/guidance"
)

type WorkflowRunnable interface {
	AssertSessionRunnable(context.Context, string) error
}
type WorkflowAsks interface {
	AnnouncePendingAsk(context.Context, string)
}
type WorkflowApprovals interface {
	HumanApprovalAwaiting(context.Context, string) (bool, error)
}
type WorkflowObligations interface {
	HostObligationHeld(context.Context, string) (bool, error)
}
type ReportDocuments interface {
	CheckRunReportDocument(context.Context, string, guidance.CoordinatorCompletionReport) ([]guidance.ReportDocumentIssue, error)
}
