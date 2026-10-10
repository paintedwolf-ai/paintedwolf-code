package mcpadmin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestProviderOperationsRefuseUnattachedProjectBeforeChangingDeviceState(t *testing.T) {
	h := testHandler(t)
	cases := []struct {
		name, body string
		handle     http.HandlerFunc
	}{
		{"list", "", h.ListProviders}, {"recipes", "", h.ListRecipes},
		{"create", "{}", h.CreateProvider}, {"update", "{}", h.UpdateProvider},
		{"delete", "", h.DeleteProvider}, {"tools", "", h.ListProviderTools},
		{"refresh", "", h.RefreshProvider}, {"check", "", h.CheckProviders},
		{"start OAuth", "", h.StartOAuth}, {"complete OAuth", `{"code":"code","state":"state"}`, h.CompleteOAuth},
		{"cancel OAuth", `{"state":"state"}`, h.CancelOAuth}, {"revoke OAuth", "", h.RevokeOAuth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/mcp/providers?project_id=unattached-project", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			tc.handle(rec, req)
			var out wire.ErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				testutil.FailErr(t, "decode unattached project refusal", err)
			}
			if rec.Code != http.StatusNotFound || out.Code != wire.ApiErrorCodeProjectNotFound {
				t.Fatalf("status=%d code=%s", rec.Code, out.Code)
			}
		})
	}
}

func TestDeviceProviderRequestsReachRegistryAndPreserveMissingProvider(t *testing.T) {
	h := testHandler(t)
	for _, handle := range []http.HandlerFunc{h.ListProviders, h.ListRecipes} {
		rec := httptest.NewRecorder()
		handle(rec, httptest.NewRequest(http.MethodGet, "/v1/mcp/providers", nil))
		if rec.Code != http.StatusOK || !json.Valid(rec.Body.Bytes()) {
			t.Fatalf("device inventory status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	for _, tc := range []struct {
		handle http.HandlerFunc
		body   string
	}{
		{h.ListProviderTools, ""}, {h.UpdateProvider, `{"enabled":true}`}, {h.DeleteProvider, ""}, {h.RefreshProvider, ""}, {h.StartOAuth, ""}, {h.CompleteOAuth, `{"code":"code","state":"state"}`}, {h.RevokeOAuth, ""},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/mcp/providers/missing", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		route := chi.NewRouteContext()
		route.URLParams.Add("provider_id", "missing")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		tc.handle(rec, req)
		var response wire.ErrorResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			testutil.FailErr(t, "decode missing provider refusal", err)
		}
		if response.Code != "mcp_provider_not_found" {
			t.Fatalf("missing provider status=%d response=%s", rec.Code, rec.Body.String())
		}
	}
}
