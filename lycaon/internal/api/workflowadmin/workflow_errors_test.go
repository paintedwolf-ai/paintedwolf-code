package workflowadmin

import (
	"fmt"
	workflowrunstate "github.com/lycaon/lycaon/internal/workflow/runstate"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
)

// A run whose pinned version left the catalog answers with its own code, not
// the unknown-workflow code its error also satisfies.
func TestMissingRunVersionAnswersWorkflowVersionUnavailable(t *testing.T) {
	h := &RunControl{responses: &httpio.Responder{Logger: slog.Default()}}
	rec := httptest.NewRecorder()
	err := fmt.Errorf("resume: %w", &workflowrunstate.WorkflowVersionUnavailableError{WorkflowID: "security-survey", Version: "1.0.0"})
	h.WriteWorkflowError(rec, httptest.NewRequest(http.MethodPost, "/v1/workflow-runs/run/resume", nil), err)
	body := rec.Body.String()
	if rec.Code != http.StatusConflict || !strings.Contains(body, "workflow_version_unavailable") || !strings.Contains(body, "security-survey") {
		t.Fatalf("status %d body %s", rec.Code, body)
	}
}
