package workflowadmin

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/report"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// serveReportPDF renders one assembled report and writes it as an attachment.
func (s *Reports) serveReportPDF(w http.ResponseWriter, r *http.Request, input report.ReportInput) {
	pdf, err := report.Render(input)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", reportFilename(input)))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(pdf); err != nil {
		s.responses.InternalError(w, r, err)
	}
}

// isRenderableCompletionReport reports whether a message is a grounded,
// transcript-visible completion from a workflow report phase.
func isRenderableCompletionReport(msg wire.Message) bool {
	if msg.Role != wire.MessageRoleAssistant || msg.Kind != wire.MessageKindCompletionReport {
		return false
	}
	vis := msg.Visibility
	if vis == "" {
		vis = wire.MessageVisibilityTranscript
	}
	if vis != wire.MessageVisibilityTranscript {
		return false
	}
	if msg.Grounding == nil {
		return false
	}
	return msg.CompletionReport != nil && msg.CompletionReport.Scope == wire.CompletionReportScopeRun
}

func reportFilename(input report.ReportInput) string {
	slug := strings.ToLower(strings.TrimSpace(input.Title))
	slug = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r == ' ', r == '_', r == '-':
			return '-'
		default:
			return -1
		}
	}, slug)
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "report"
	}
	short := strings.TrimSpace(input.RunID)
	if len(short) > 8 {
		short = short[:8]
	}
	date := "unknown"
	if t, err := time.Parse(time.RFC3339, input.CompletedAt); err == nil {
		date = t.UTC().Format("20060102")
	}
	return fmt.Sprintf("painted-wolf-code-%s-%s-%s.pdf", slug, short, date)
}
