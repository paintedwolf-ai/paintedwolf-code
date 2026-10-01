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

	delegation := api.Delegation{
		ID:        fixtureDelegationID,
		ProjectID: fixtureProjectID,
		Task:      "fixture task",
		Strategy:  api.HuntStrategyFileBased,
		Status:    "active",
		Phase:     api.DelegationPhaseSetup,
		CreatedAt: now,
	}
	leg := api.Leg{
		ID:           fixtureLegID,
		DelegationID: fixtureDelegationID,
		Title:        "leg-1",
		Status:       api.LegStatusPending,
		CreatedAt:    now,
	}

	mux.HandleFunc("POST /v1/delegations", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, delegation)
	})
	mux.HandleFunc("GET /v1/delegations/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, delegation)
	})
	mux.HandleFunc("GET /v1/delegations/{id}/legs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.DelegationLegListResponse{Legs: []api.Leg{leg}})
	})
	mux.HandleFunc("POST /v1/delegations/{id}/dispatch", func(w http.ResponseWriter, r *http.Request) {
		dispatched := leg
		dispatched.Status = api.LegStatusDispatched
		writeJSON(w, http.StatusOK, dispatched)
	})
	mux.HandleFunc("POST /v1/delegations/{id}/abort", func(w http.ResponseWriter, r *http.Request) {
		aborted := delegation
		aborted.Status = "aborted"
		writeJSON(w, http.StatusOK, aborted)
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
	mux.HandleFunc("POST /v1/workers/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		wt := worker
		wt.Status = api.WorkerStatusCanceled
		writeJSON(w, http.StatusAccepted, wt)
	})
}
