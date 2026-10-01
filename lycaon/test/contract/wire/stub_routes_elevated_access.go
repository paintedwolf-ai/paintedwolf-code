package contract

import (
	"net/http"
	"sync"

	"github.com/lycaon/lycaon/pkg/api"
)

func registerStubElevatedAccessRoutes(mux *http.ServeMux, writeJSON stubJSONWriter) {
	var mu sync.Mutex
	summary := api.ElevatedAccessSummary{
		RootSessionID: fixtureSessionID, ApprovalsEnabled: true, Total: 2,
		Records: []api.ElevatedAccessRecord{
			{ID: "grant_local_service", Kind: "grant", Title: "Allow local service", Scope: api.ApprovalGrantScopeDevice, Effects: []api.ElevatedAccessEffect{api.ElevatedAccessEffectLocalService}},
			{ID: "quiet_direct_network", Kind: "quiet", Title: "Allow direct network", Scope: api.ApprovalGrantScopeChat, Effects: []api.ElevatedAccessEffect{api.ElevatedAccessEffectDirectNetwork}},
		},
		SharedScopes: []api.ApprovalGrantScope{api.ApprovalGrantScopeDevice},
	}
	sessionExists := func(w http.ResponseWriter, r *http.Request) bool {
		if r.PathValue("id") == fixtureSessionID {
			return true
		}
		writeJSON(w, http.StatusNotFound, api.ErrorResponse{Code: api.ApiErrorCodeSessionNotFound, Message: "Session not found."})
		return false
	}
	mux.HandleFunc("GET /v1/sessions/{id}/elevated-access", func(w http.ResponseWriter, r *http.Request) {
		if !sessionExists(w, r) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		writeJSON(w, http.StatusOK, summary)
	})
	mux.HandleFunc("POST /v1/sessions/{id}/elevated-access/revoke", func(w http.ResponseWriter, r *http.Request) {
		if !sessionExists(w, r) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		results := make([]api.ElevatedAccessRevokeResult, 0, summary.Total)
		for _, record := range summary.Records {
			results = append(results, api.ElevatedAccessRevokeResult{ID: record.ID, Disposition: "revoked"})
		}
		summary.Total = 0
		summary.Records = []api.ElevatedAccessRecord{}
		summary.SharedScopes = []api.ApprovalGrantScope{}
		writeJSON(w, http.StatusOK, api.RevokeElevatedAccessResponse{Results: results, Remaining: summary})
	})
}
