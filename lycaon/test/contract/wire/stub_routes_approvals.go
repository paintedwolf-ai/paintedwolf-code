package contract

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

func registerStubPolicyApprovalRoutes(mux *http.ServeMux, writeJSON stubJSONWriter) {
	registerStubElevatedAccessRoutes(mux, writeJSON)
	mux.HandleFunc("GET /v1/settings/model-policy", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ModelPolicy{
			Coordinator: &api.ModelRefDTO{ProviderID: "anthropic", Model: "claude"},
			Lite:        &api.ModelRefDTO{ProviderID: "anthropic", Model: "claude-haiku"},
			AgentPool: api.AgentPoolDTO{
				Selection: "round_robin",
				Models:    []api.ModelRefDTO{{ProviderID: "anthropic", Model: "claude"}},
			},
		})
	})
	mux.HandleFunc("PATCH /v1/settings/model-policy", func(w http.ResponseWriter, r *http.Request) {
		var patch api.ModelPolicyPatch
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Code: api.ApiErrorCodeInvalidRequest, Message: err.Error()})
			return
		}
		if patch.Coordinator == nil && patch.Lite == nil && patch.AgentPool == nil {
			writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Code: api.ApiErrorCodeInvalidRequest, Message: "empty patch"})
			return
		}
		body := api.ModelPolicy{
			Coordinator: &api.ModelRefDTO{ProviderID: "anthropic", Model: "claude"},
			Lite:        &api.ModelRefDTO{ProviderID: "anthropic", Model: "claude-haiku"},
			AgentPool: api.AgentPoolDTO{
				Selection: "round_robin",
				Models:    []api.ModelRefDTO{{ProviderID: "anthropic", Model: "claude"}},
			},
		}
		if patch.Coordinator != nil {
			body.Coordinator = patch.Coordinator
		}
		if patch.Lite != nil {
			body.Lite = patch.Lite
		}
		if patch.AgentPool != nil {
			body.AgentPool = *patch.AgentPool
		}
		writeJSON(w, http.StatusOK, body)
	})
	permFixture := api.ApprovalConfigResponse{
		Scope:           api.SettingsScopeGlobal,
		Rules:           []api.ApprovalRule{{Category: api.ApprovalCategoryTool, Pattern: "command", Effect: api.ApprovalEffectAsk}},
		ManagedRules:    []api.ManagedApprovalRule{},
		MergedFrom:      []string{"bundled"},
		ApprovalPosture: "balanced",
	}
	limitsFixture := api.SettingsLimitsResponse{
		Scope:                        api.SettingsScopeGlobal,
		MaxIterations:                10,
		OverlayPromoteMaxIterations:  30,
		MaxToolResultBytes:           65536,
		SessionSpendCeilingNanoUSD:   0,
		LLMTurnTimeoutMs:             10800000,
		CoordinatorHostTurnTimeoutMs: 10800000,
		CoordinatorMaxSleepMs:        10800000,
		AwaitParentWorkersTimeoutMs:  10800000,
		WorkerToolBudgetDefault:      20,
		WorkerToolBudgetMin:          2,
		WorkerToolBudgetMax:          120,
		MergedFrom:                   []string{"bundled"},
	}

	mux.HandleFunc("GET /v1/settings/approvals", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, permFixture)
	})
	mux.HandleFunc("PATCH /v1/settings/approvals", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, permFixture)
	})
	mux.HandleFunc("GET /v1/approval-asks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ApprovalRecentAsksResponse{WindowDays: 7, SinceAt: time.Now().UTC().Add(-7 * 24 * time.Hour), Asks: []api.ApprovalRecentAskRow{}})
	})
	mux.HandleFunc("GET /v1/approval-grants", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ApprovalGrantsResponse{Grants: []api.ApprovalGrant{}})
	})
	mux.HandleFunc("POST /v1/approval-grants", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, api.ApprovalGrant{
			ID: "grant_socket", Scope: api.ApprovalGrantScopeDevice,
			Category: api.ApprovalGrantCategorySocketPath, Pattern: "/tmp/s.sock",
			Title: "Allow local service", Coverage: "/tmp/s.sock",
			GrantedAt: time.Now().UTC(), ExpiresWhen: "when revoked", ReaskWhen: "target changes",
			ApprovedPath: "/tmp/s.sock", ResolvedPath: "/tmp/s.sock",
			EffectiveAuthority: api.SocketCapabilityAuthorityOutsideSandboxDaemon,
			AuthorityWarning:   api.SocketGrantAuthorityWarning,
			RevokeAppliesTo:    api.SocketGrantRevokeAppliesTo,
			Source:             "settings",
		})
	})
	mux.HandleFunc("POST /v1/approval-grants/resolve-socket", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ResolveSocketGrantResponse{
			ApprovedPath:       "/tmp/s.sock",
			ResolvedPath:       "/tmp/s.sock",
			EffectiveAuthority: api.SocketCapabilityAuthorityOutsideSandboxDaemon,
			AuthorityWarning:   api.SocketGrantAuthorityWarning,
		})
	})
	mux.HandleFunc("POST /v1/approval-grants/revoke", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.RevokeApprovalGrantsResponse{
			Results: []api.ApprovalGrantRevokeResult{{ID: "grant_socket", Revoked: true}},
		})
	})
	detectionPackFixture := api.DetectionPack{
		ID:          "aws-cli",
		Label:       "AWS CLI",
		Description: "fixture",
		Source:      "bundled",
		Enabled:     true,
		Rules: []api.DetectionRuleSummary{{
			ID:        "11111111-1111-4111-8111-111111111111",
			Title:     "fixture rule",
			Level:     "high",
			Supported: true,
		}},
	}
	mux.HandleFunc("GET /v1/detection-packs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.DetectionPackList{
			Packs:    []api.DetectionPack{detectionPackFixture},
			Rejected: map[string]api.DetectionPackRejectedRow{},
		})
	})
	mux.HandleFunc("PATCH /v1/detection-packs/{pack_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, detectionPackFixture)
	})
	mux.HandleFunc("POST /v1/detection-packs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, api.DetectionPackImportResult{
			DryRun:        false,
			Pack:          detectionPackFixture,
			RejectedRules: []api.DetectionPackRejectedRule{},
			Ignored:       []string{},
		})
	})
	mux.HandleFunc("DELETE /v1/detection-packs/{pack_id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/settings/limits", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, limitsFixture)
	})
	mux.HandleFunc("PATCH /v1/settings/limits", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, limitsFixture)
	})
}
