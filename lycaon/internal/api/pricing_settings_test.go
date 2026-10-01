package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/cost/costtest"
	"github.com/lycaon/lycaon/internal/modelfeed"
	"github.com/lycaon/lycaon/internal/pricing"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func pricingNoNetworkFetch(context.Context, string) ([]byte, error) {
	return nil, errors.New("no network in pricing settings tests")
}

// withTestPricingHost serves pricing through a host over the settings pricing store.
func withTestPricingHost(t *testing.T) testDeps {
	t.Helper()
	return func(d *Dependencies) {
		if d.Settings == nil || d.Settings.Pricing == nil {
			t.Fatal("settings pricing store missing")
		}
		mem := costtest.NewTracker(t, cost.NoopPricer{})
		body, err := os.ReadFile(filepath.Join(configlayout.FindModuleRoot(), "internal", "pricing", "testdata", "models-dev", "api.json"))
		testutil.FailErr(t, "read models-dev fixture", err)
		doc, err := modelfeed.ParseDocument(body)
		testutil.FailErr(t, "ParseDocument", err)
		host := &settings.PricingHost{
			Store:     d.Settings.Pricing,
			Catalog:   pricing.SourcesConfig{},
			CacheDir:  filepath.Join(t.TempDir(), "pricing-cache"),
			Tracker:   mem,
			GetBytes:  pricingNoNetworkFetch,
			ModelFeed: &apiStubFeed{doc: doc},
		}
		// Catalog rows come from the store; registry only needs Sources when tracking turns on.
		for _, ent := range d.Settings.Pricing.Catalog() {
			host.Catalog.Sources = append(host.Catalog.Sources, pricing.SourceConfig{
				ID:    ent.ID,
				Kind:  ent.Kind,
				Label: ent.Label,
				URL:   ent.URL,
			})
		}
		testutil.FailErr(t, "SyncFromStore", host.SyncFromStore(t.Context()))
		d.Pricing = host
		t.Cleanup(func() {
			host.Close()
		})
	}
}

type apiStubFeed struct {
	doc *modelfeed.Document
}

func (s *apiStubFeed) Document(context.Context) (*modelfeed.Document, error) { return s.doc, nil }
func (s *apiStubFeed) Refresh(context.Context) (*modelfeed.Document, error)  { return s.doc, nil }
func (s *apiStubFeed) Snapshot() (*modelfeed.Document, string, bool) {
	return s.doc, string(pricing.StatusOK), s.doc != nil
}

func TestGetPutPricingHTTP(t *testing.T) {
	srv, _, _ := newSettingsTestServer(t, withTestPricingHost(t))

	getReq := newAuthedRequest(http.MethodGet, "/v1/settings/pricing", nil)
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
	putReq := newAuthedRequest(http.MethodPatch, "/v1/settings/pricing", strings.NewReader(putBody))
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
	badReq := newAuthedRequest(http.MethodPatch, "/v1/settings/pricing", strings.NewReader(badBody))
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
	srv, _, _ := newSettingsTestServer(t, withTestPricingHost(t))

	req := newAuthedRequest(http.MethodPost, "/v1/settings/pricing/sources/not-a-source/refresh", http.NoBody)
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
