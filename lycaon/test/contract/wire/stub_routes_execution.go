package contract

import (
	"net/http"
	"time"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/pkg/api"
)

func registerStubExecutionRoutes(mux *http.ServeMux, now time.Time, writeJSON stubJSONWriter) {
	registerStubBoardSearchRoutes(mux, now, writeJSON)
	registerStubScanRoutes(mux, now, writeJSON)
	registerStubScannerRoutes(mux, writeJSON)
	registerStubIntegrationRoutes(mux, writeJSON)
	registerStubContributionExtensionRoutes(mux, writeJSON)

	plan := api.Blueprint{
		ID:        fixtureBlueprintID,
		ProjectID: fixtureProjectID,
		Title:     "fixture-plan",
		Path:      settingsoverlay.Rel("blueprints/fixture-plan.md"),
		Content:   "# Plan",
		Status:    api.BlueprintStatusDraft,
		Version:   1,
		UpdatedAt: now,
	}
	planSummary := api.BlueprintSummary{
		ID:        plan.ID,
		Title:     plan.Title,
		Path:      plan.Path,
		Status:    plan.Status,
		Version:   plan.Version,
		UpdatedAt: now,
	}

	mux.HandleFunc("GET /v1/projects/{id}/blueprints", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.BlueprintListResponse{Blueprints: []api.BlueprintSummary{planSummary}})
	})
	mux.HandleFunc("POST /v1/projects/{id}/blueprints", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, plan)
	})
	mux.HandleFunc("GET /v1/projects/{id}/blueprints/{blueprint_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, plan)
	})
	mux.HandleFunc("PATCH /v1/projects/{id}/blueprints/{blueprint_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, plan)
	})
	mux.HandleFunc("DELETE /v1/projects/{id}/blueprints/{blueprint_id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/projects/{id}/blueprints/{blueprint_id}/approve", func(w http.ResponseWriter, r *http.Request) {
		approved := plan
		approved.Status = api.BlueprintStatusApproved
		writeJSON(w, http.StatusOK, approved)
	})
	mux.HandleFunc("POST /v1/projects/{id}/blueprints/{blueprint_id}/launch", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, api.LaunchBlueprintResponse{
			SessionID:     fixtureSessionID,
			WorkflowRunID: fixtureWorkflowRunID,
			BlueprintID:   fixtureBlueprintID,
		})
	})

	worker := api.WorkerTask{
		ID:        fixtureWorkerID,
		AgentType: "implementer",
		Status:    api.WorkerStatusPending,
		CreatedAt: now,
	}

	mux.HandleFunc("GET /v1/workers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.WorkerListResponse{Workers: []api.WorkerTask{worker}})
	})
	mux.HandleFunc("GET /v1/workers/history", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.WorkerHistoryResponse{
			WorkerID:  fixtureWorkerID,
			Name:      "implementer-1",
			AgentType: "implementer",
			State:     api.WorkerStateWorking,
			Rounds: []api.WorkerHistoryRound{{
				Ordinal:   1,
				WorkerID:  fixtureWorkerID,
				Status:    api.WorkerStatusRunning,
				CreatedAt: now.Format("2006-01-02T15:04:05Z07:00"),
			}},
			Messages: []api.WorkerHistoryMessage{},
		})
	})
	mux.HandleFunc("POST /v1/workers/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		wt := worker
		wt.Status = api.WorkerStatusCanceled
		writeJSON(w, http.StatusAccepted, wt)
	})
}
