package contract

import (
	"net/http"
	"time"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/pkg/api"
)

func registerStubWorkflowRoutes(mux *http.ServeMux, now time.Time, writeJSON stubJSONWriter) {
	workflowRun := api.WorkflowRun{
		ID:              fixtureWorkflowRunID,
		SessionID:       fixtureSessionID,
		ProjectID:       fixtureProjectID,
		WorkflowID:      "plan",
		WorkflowVersion: "1.0.0",
		Status:          api.WorkflowRunStatusRunning,
		Revision:        1,
		CurrentPhase:    "stub",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	workflowCatalog := []api.WorkflowSummary{{
		ID:             "plan",
		Version:        "1.0.0",
		Name:           "plan",
		Trigger:        "/plan",
		InitialPosture: "spec",
		Phases:         []string{"stub", "research", "approve", "implement"},
		Scope:          api.WorkflowScopeBundled,
	}}

	mux.HandleFunc("GET /v1/workflows", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.WorkflowListResponse{Workflows: workflowCatalog})
	})
	mux.HandleFunc("POST /v1/sessions/{id}/workflows/compose", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, api.ComposeWorkflowResponse{
			Summary: api.WorkflowSummary{
				ID:             "hotfix-session",
				Version:        "1.0.0",
				Name:           "hotfix-session",
				Scope:          api.WorkflowScopeSession,
				Phases:         []string{"intake", "build"},
				InitialPosture: "spec",
			},
			EffectiveYAML: "id: hotfix-session\nversion: 1.0.0\n",
			EffectiveSummary: api.ComposeEffectiveSummary{
				Extends:          "plan@1.0.0",
				CoordinatorBrief: "Skips parent phases research and approve.",
				Phases: []api.ComposePhaseSummary{
					{ID: "stub", Next: "implement"},
					{ID: "implement", CompleteWhen: "gates_satisfied"},
				},
				PhasesRemovedFromParent: []string{"research", "approve"},
				GatesByPhase:            map[string][]string{"implement": {"delegation_closeout_complete"}},
			},
		})
	})
	mux.HandleFunc("GET /v1/workflow-templates", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.WorkflowTemplateListResponse{
			Templates: []api.WorkflowTemplateSummary{{
				ID:          "hotfix-template",
				Description: "Minimal stub to implement hotfix extending plan",
				Parameters: map[string]api.WorkflowTemplateParameter{
					"workflow_id": {Type: "string", Required: true},
				},
			}},
		})
	})
	mux.HandleFunc("POST /v1/sessions/{id}/workflows/compose-from-template", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, api.ComposeWorkflowResponse{
			Summary: api.WorkflowSummary{
				ID:      "e2e-hotfix",
				Version: "1.0.0",
				Name:    "e2e-hotfix",
				Scope:   api.WorkflowScopeSession,
				Phases:  []string{"stub", "implement"},
			},
			EffectiveYAML: "id: e2e-hotfix\nversion: 1.0.0\n",
			EffectiveSummary: api.ComposeEffectiveSummary{
				Extends:          "plan@1.0.0",
				CoordinatorBrief: "Extends plan@1.0.0.",
				Phases:           []api.ComposePhaseSummary{{ID: "stub"}, {ID: "implement"}},
			},
		})
	})
	mux.HandleFunc("POST /v1/sessions/{id}/workflows/{workflow_id}/persist", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, api.PersistWorkflowResponse{
			Path: settingsoverlay.Rel("workflows/hotfix-session.yaml"),
			Summary: api.WorkflowSummary{
				ID:      "hotfix-session",
				Version: "1.0.0",
				Name:    "hotfix-session",
				Scope:   api.WorkflowScopeProject,
				Trigger: "/hotfix-session",
			},
		})
	})
	mux.HandleFunc("GET /v1/sessions/{id}/workflow-runs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.WorkflowRunPage{Runs: []api.WorkflowRun{workflowRun}})
	})
	mux.HandleFunc("POST /v1/sessions/{id}/workflow-runs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, workflowRun)
	})
	mux.HandleFunc("POST /v1/workflow-runs/{id}/exit", func(w http.ResponseWriter, r *http.Request) {
		run := workflowRun
		run.Status = api.WorkflowRunStatusCanceled
		writeJSON(w, http.StatusOK, run)
	})
	mux.HandleFunc("GET /v1/sessions/{id}/workflow-runs/active", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ActiveWorkflowRunResponse{Run: &workflowRun})
	})
	mux.HandleFunc("GET /v1/workflow-runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, workflowRun)
	})
	mux.HandleFunc("GET /v1/workflow-runs/{id}/report", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `attachment; filename="painted-wolf-code-security-survey-stub-20260708.pdf"`)
		_, _ = w.Write([]byte("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n1 0 obj<<>>endobj\ntrailer<<>>\n%%EOF\n"))
	})
	mux.HandleFunc("POST /v1/workflow-runs/{id}/pause", func(w http.ResponseWriter, r *http.Request) {
		run := workflowRun
		run.Status = api.WorkflowRunStatusPaused
		writeJSON(w, http.StatusOK, run)
	})
	mux.HandleFunc("POST /v1/workflow-runs/{id}/resume", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, workflowRun)
	})
	mux.HandleFunc("POST /v1/workflow-runs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		run := workflowRun
		run.Status = api.WorkflowRunStatusCanceled
		writeJSON(w, http.StatusOK, run)
	})
	mux.HandleFunc("POST /v1/workflow-runs/{id}/advance", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, workflowRun)
	})
	mux.HandleFunc("POST /v1/workflow-runs/{id}/transitions/{transition_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, workflowRun)
	})
	mux.HandleFunc("POST /v1/workflow-runs/{id}/decisions/{phase_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, workflowRun)
	})
	mux.HandleFunc("POST /v1/workflow-runs/{id}/feedback/{phase_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, workflowRun)
	})
	mux.HandleFunc("POST /v1/workflow-runs/{id}/feedback/{phase_id}/secret", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, workflowRun)
	})
}
