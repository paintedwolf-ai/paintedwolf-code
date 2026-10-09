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
type WorkflowHolds interface {
	HumanApprovalAwaiting(context.Context, string) (bool, error)
	HostObligationHeld(context.Context, string) (bool, error)
}
type ReportDocuments interface {
	CheckRunReportDocument(context.Context, string, guidance.CoordinatorCompletionReport) ([]guidance.ReportDocumentIssue, error)
}
