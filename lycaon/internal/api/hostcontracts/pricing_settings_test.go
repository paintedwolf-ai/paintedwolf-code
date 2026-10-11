package hostcontracts

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestGetPutPricingHTTP(t *testing.T) {
	srv, _, _ := contractfixture.NewSettingsTestServer(t, contractfixture.WithTestPricingHost(t))

	getReq := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/settings/pricing", nil)
	getW := httptest.NewRecorder()
	srv.ServeHTTP(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", getW.Code, getW.Body.String())
	}
	var got wire.SettingsPricingResponse
	if err := json.Unmarshal(getW.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if !got.CostTrackingEnabled {
		t.Fatal("expected tracking on by default")
	}
	selected := 0
	for _, src := range got.AvailableSources {
		if src.Enabled {
			selected++
		}
	}
	if selected != 1 {
		t.Fatalf("selected pricing sources = %d, want 1", selected)
	}
	if len(got.AvailableSources) == 0 {
		t.Fatal("expected available_sources from catalog")
	}

	putBody := `{"cost_tracking_enabled":true,"sources":[{"id":"models-dev","enabled":true},{"id":"litellm","enabled":false},{"id":"ai-pricing-fyi","enabled":false}]}`
	putReq := contractfixture.NewAuthedRequest(http.MethodPatch, "/v1/settings/pricing", strings.NewReader(putBody))
	putReq.Header.Set("Content-Type", "application/json")
	putW := httptest.NewRecorder()
	srv.ServeHTTP(putW, putReq)
	if putW.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", putW.Code, putW.Body.String())
	}
	var updated wire.SettingsPricingResponse
	if err := json.Unmarshal(putW.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode PUT: %v", err)
	}
	if !updated.CostTrackingEnabled || updated.CostTrackingSinceAt == nil {
		t.Fatalf("PUT not applied: %+v", updated)
	}

	badBody := `{"cost_tracking_enabled":true,"sources":[{"id":"models-dev","enabled":false},{"id":"litellm","enabled":false},{"id":"ai-pricing-fyi","enabled":false}]}`
	badReq := contractfixture.NewAuthedRequest(http.MethodPatch, "/v1/settings/pricing", strings.NewReader(badBody))
	badReq.Header.Set("Content-Type", "application/json")
	badW := httptest.NewRecorder()
	srv.ServeHTTP(badW, badReq)
	if badW.Code != http.StatusBadRequest {
		t.Fatalf("PUT no-source status = %d body=%s", badW.Code, badW.Body.String())
	}
	var errResp wire.ErrorResponse
	if err := json.Unmarshal(badW.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if errResp.Code != "pricing_no_source" {
		t.Fatalf("code = %q want pricing_no_source", errResp.Code)
	}
}

func TestRefreshUnknownSource(t *testing.T) {
	srv, _, _ := contractfixture.NewSettingsTestServer(t, contractfixture.WithTestPricingHost(t))

	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/settings/pricing/sources/not-a-source/refresh", http.NoBody)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var errResp wire.ErrorResponse
	body, _ := io.ReadAll(w.Body)
	if err := json.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if errResp.Code != wire.ApiErrorCodePricingSourceNotFound {
		t.Fatalf("code = %q want pricing_source_not_found", errResp.Code)
	}
}
