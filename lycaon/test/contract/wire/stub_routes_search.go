package contract

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

func registerStubBoardSearchRoutes(mux *http.ServeMux, now time.Time, writeJSON stubJSONWriter) {
	board := api.BoardView{
		Summary: "No workers.",
		Repo: api.RepoBrief{
			Languages:   []string{"Go"},
			FileCount:   1,
			GeneratedAt: now,
		},
		Cost:            nil,
		PackContentHash: "hash",
		DetailLevel:     api.BoardDetailLevelCompact,
		GeneratedAt:     now.Format(time.RFC3339),
		NowLine:         "Now: stub",
	}

	mux.HandleFunc("GET /v1/projects/{id}/board", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, board)
	})
	mux.HandleFunc("POST /v1/search", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SearchResponse{
			Hits:             []api.SearchHit{},
			Status:           api.SearchResultStatusComplete,
			Exhaustive:       true,
			CountRelation:    api.SearchCountRelationExact,
			FacetsExhaustive: true,
			Interpreted:      api.SearchInterpretation{Scope: api.SearchScopeGlobal},
		})
	})
	mux.HandleFunc("POST /v1/search/replace/preview", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SearchReplacePreviewResponse{
			State:     api.SearchReplacePreviewReady,
			Issues:    []api.SearchIssue{},
			Files:     []api.SearchReplaceFilePreview{},
			Truncated: false,
		})
	})
	mux.HandleFunc("POST /v1/search/replace/apply", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SearchReplaceApplyResponse{
			Files: []api.SearchReplaceFileOutcome{},
		})
	})
	mux.HandleFunc("POST /v1/search/export", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("X-Export-Truncated", "false")
		w.Header().Set("Content-Disposition", `attachment; filename="search-export-global-stub.jsonl"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}\n"))
	})
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		payload, _ := json.Marshal(api.SessionEvent{
			ID:        fixtureSessionID,
			ProjectID: fixtureProjectID,
			Action:    api.SessionEventActionUpdated,
			Status:    api.SessionStatusIdle,
		})
		envelope, _ := json.Marshal(api.EventEnvelope{
			V:           1,
			Topic:       api.EventTopicSession,
			PublishedAt: now,
			Scope:       api.EventScope{Kind: api.EventScopeSession, ProjectID: fixtureProjectID, SessionID: fixtureSessionID},
			Data:        payload,
		})
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(envelope)
		_, _ = w.Write([]byte("\n\n"))
	})

	mux.HandleFunc("GET /v1/cost/summary", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.CostSummary{
			Scope:             api.CostScopeSession,
			SessionID:         fixtureSessionID,
			EstimatedNanoUsd:  420000000,
			TokenTotals:       api.TokenTotals{Prompt: 1200, Completion: 800},
			Coordinator:       api.CostBreakdown{EstimatedNanoUsd: 300000000, TokenTotals: api.TokenTotals{Prompt: 800, Completion: 500}},
			Workers:           api.CostBreakdown{EstimatedNanoUsd: 120000000, TokenTotals: api.TokenTotals{Prompt: 400, Completion: 300}, TaskCount: 2},
			Summarizer:        api.CostBreakdown{EstimatedNanoUsd: 0, TokenTotals: api.TokenTotals{}},
			PricingProvenance: []api.CostPricingProvenance{},
			EstimateCoverage:  api.CostEstimateComplete,
		})
	})
	mux.HandleFunc("GET /v1/projects/{id}/cost-report", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ProjectCostReport{
			Summary: api.CostSummary{
				Scope:             api.CostScopeProject,
				ProjectID:         fixtureProjectID,
				TokenTotals:       api.TokenTotals{},
				Coordinator:       api.CostBreakdown{TokenTotals: api.TokenTotals{}},
				Workers:           api.CostBreakdown{TokenTotals: api.TokenTotals{}},
				Summarizer:        api.CostBreakdown{TokenTotals: api.TokenTotals{}},
				PricingProvenance: []api.CostPricingProvenance{},
				EstimateCoverage:  api.CostEstimateComplete,
			},
			ProjectUtilities: api.CostSummary{
				Scope:             api.CostScopeProject,
				ProjectID:         fixtureProjectID,
				TokenTotals:       api.TokenTotals{},
				Coordinator:       api.CostBreakdown{TokenTotals: api.TokenTotals{}},
				Workers:           api.CostBreakdown{TokenTotals: api.TokenTotals{}},
				Summarizer:        api.CostBreakdown{TokenTotals: api.TokenTotals{}},
				PricingProvenance: []api.CostPricingProvenance{},
				EstimateCoverage:  api.CostEstimateComplete,
			},
			RetiredSessions: api.CostSummary{
				Scope:             api.CostScopeProject,
				ProjectID:         fixtureProjectID,
				TokenTotals:       api.TokenTotals{},
				Coordinator:       api.CostBreakdown{TokenTotals: api.TokenTotals{}},
				Workers:           api.CostBreakdown{TokenTotals: api.TokenTotals{}},
				Summarizer:        api.CostBreakdown{TokenTotals: api.TokenTotals{}},
				PricingProvenance: []api.CostPricingProvenance{},
				EstimateCoverage:  api.CostEstimateComplete,
			},
			Sessions: []api.ProjectCostSession{},
		})
	})
}
