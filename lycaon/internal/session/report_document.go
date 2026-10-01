package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/guidance"
)

// ReportDocumentChecker checks a closeout that delivers its workflow run's
// report against the run: its claims, declared rating, and scan inventory.
type ReportDocumentChecker interface {
	CheckRunReportDocument(ctx context.Context, sessionID string, report guidance.CoordinatorCompletionReport) ([]guidance.ReportDocumentIssue, error)
}

// SetReportDocumentChecker wires the run report document check.
func (m *Manager) SetReportDocumentChecker(checker ReportDocumentChecker) {
	if m != nil {
		m.reportDocuments = checker
	}
}
