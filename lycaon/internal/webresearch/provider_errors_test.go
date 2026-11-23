package webresearch

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

func TestClassifyProviderErrorAcrossCatalog(t *testing.T) {
	cat := testCatalog(t)
	spec := func(id string) *ErrorSpec {
		entry, ok := cat.Entry(id)
		if !ok {
			t.Fatalf("catalog has no %s", id)
		}
		return entry.Errors
	}
	for _, tc := range []struct {
		provider string
		status   int
		body     string
		keyed    bool
		state    ProviderErrorState
		code     string
	}{
		{"brave", 422, `{"type":"ErrorResponse","error":{"code":"SUBSCRIPTION_TOKEN_INVALID","detail":"The provided subscription token is invalid.","meta":{"component":"authentication"},"status":422}}`, true, ErrorCredentialRejected, "SUBSCRIPTION_TOKEN_INVALID"},
		{"brave", 422, `{"error":{"code":"OPTION_NOT_IN_PLAN","meta":{"component":"authentication"}}}`, true, ErrorCredentialRejected, "authentication"},
		{"brave", 429, `{"error":{"code":"QUOTA_LIMITED"}}`, true, ErrorQuotaExhausted, "QUOTA_LIMITED"},
		{"brave", 429, `{"error":{"code":"RATE_LIMITED"}}`, true, ErrorRateLimited, "RATE_LIMITED"},
		{"brave", 500, `{"error":{"code":"INTERNAL"}}`, true, ErrorUnavailable, "INTERNAL"},
		{"brave", 422, `{"error":{"code":"VALIDATION"}}`, true, ErrorRequestRejected, "VALIDATION"},
		{"google_cse", 400, `{"error":{"code":400,"details":[{"reason":"API_KEY_INVALID"}]}}`, true, ErrorCredentialRejected, "API_KEY_INVALID"},
		{"google_cse", 400, `{"error":{"errors":[{"reason":"keyInvalid"}]}}`, true, ErrorCredentialRejected, "keyInvalid"},
		{"google_cse", 403, `{"error":{"errors":[{"reason":"dailyLimitExceeded"}]}}`, true, ErrorQuotaExhausted, "dailyLimitExceeded"},
		{"google_cse", 403, `{"error":{"errors":[{"reason":"userRateLimitExceeded"}]}}`, true, ErrorRateLimited, "userRateLimitExceeded"},
		{"google_cse", 403, `{"error":{"message":"Forbidden"}}`, true, ErrorRequestRejected, "http_403"},
		{"tavily", 401, `{"detail":{"error":"Unauthorized: missing or invalid API key."}}`, true, ErrorCredentialRejected, "http_401"},
		{"tavily", 432, `{"detail":{"error":"plan limit"}}`, true, ErrorQuotaExhausted, "http_432"},
		{"marginalia", 401, ``, true, ErrorCredentialRejected, "http_401"},
		{"kagi", 403, ``, true, ErrorCredentialRejected, "http_403"},
		{"serper", 429, ``, true, ErrorRateLimited, "http_429"},
		{"serper", 502, ``, true, ErrorUnavailable, "http_502"},
		{"github", 401, `{"message":"Bad credentials"}`, true, ErrorCredentialRejected, "http_401"},
		{"github", 403, `{"message":"API rate limit exceeded"}`, true, ErrorRateLimited, "http_403"},
		{"github", 403, `{"message":"API rate limit exceeded"}`, false, ErrorRateLimited, "http_403"},
		{"stackexchange", 400, `{"error_id":400,"error_name":"bad_parameter","error_message":"key"}`, true, ErrorCredentialRejected, "bad_parameter"},
		{"stackexchange", 400, `{"error_id":400,"error_name":"bad_parameter","error_message":"site"}`, false, ErrorRequestRejected, "bad_parameter"},
		{"stackexchange_superuser", 400, `{"error_id":502,"error_name":"throttle_violation"}`, false, ErrorRateLimited, "throttle_violation"},
		{"searxng", 401, ``, true, ErrorCredentialRejected, "http_401"},
		{"searxng", 401, ``, false, ErrorRequestRejected, "http_401"},
		{"ietf_rfc", 503, ``, false, ErrorUnavailable, "http_503"},
		{"brave", 422, `{"error":{"code":"token sk-live-0123456789abcdef is invalid"}}`, true, ErrorRequestRejected, "http_422"},
	} {
		got := classifyProviderError(spec(tc.provider), tc.status, []byte(tc.body), tc.keyed)
		if got.State != tc.state || got.Code != tc.code {
			t.Errorf("%s %d keyed=%v %s = %+v, want %s/%s", tc.provider, tc.status, tc.keyed, tc.body, got, tc.state, tc.code)
		}
		if !tc.keyed && got.State == ErrorCredentialRejected {
			t.Errorf("%s keyless call classified as credential_rejected", tc.provider)
		}
	}
}

func TestClassifyProviderErrorReadsBoundedBody(t *testing.T) {
	spec := &ErrorSpec{CodePaths: []string{"error.code"}, Codes: map[string]ProviderErrorState{"KEY": ErrorCredentialRejected}}
	body := `{"pad":"` + strings.Repeat("x", providerErrorBodyLimit) + `","error":{"code":"KEY"}}`
	if got := classifyProviderError(spec, 400, []byte(body), true); got.Code != "http_400" || got.State != ErrorRequestRejected {
		t.Fatalf("oversized body = %+v", got)
	}
}

func TestRESTProviderReportsTypedErrorWithoutBodyProse(t *testing.T) {
	srv := withProviderHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":{"code":"SUBSCRIPTION_TOKEN_INVALID","detail":"The provided subscription token is invalid.","meta":{"component":"authentication"}}}`))
	})
	spec := braveRESTSpec()
	spec.BuildURL = func(Settings, string, int) (string, error) { return srv.URL + "/res/v1/web/search?q=x", nil }
	p := NewRESTSearchProvider(spec, KindKeyed, srv.exchange)
	entry, _ := testCatalog(t).Entry("brave")
	p.errors = entry.Errors
	out := p.Search(context.Background(), Settings{Keys: map[string]string{"brave": "rejected"}, PerProviderTimeoutSec: 5}, "x", 5)
	if out.reason != string(ErrorCredentialRejected) || out.errorCode != "SUBSCRIPTION_TOKEN_INVALID" || out.detail != "HTTP 422 SUBSCRIPTION_TOKEN_INVALID" {
		t.Fatalf("out = %+v", out)
	}
}

func TestSharedSlotRejectionStopsDialingUntilCredentialChanges(t *testing.T) {
	var calls atomic.Int32
	srv := withProviderHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error_id":403,"error_name":"access_denied","error_message":"bad key"}`))
	})
	reg := testRegistry(t, srv.exchange)
	var ids []string
	for _, entry := range reg.Catalog().Entries() {
		if credentialSlot(entry) == "stackexchange" {
			ids = append(ids, entry.ID)
		}
	}
	if len(ids) != 6 {
		t.Fatalf("stackexchange slot entries = %v", ids)
	}
	settings := Settings{Keys: map[string]string{}, Config: map[string]map[string]string{}, PerProviderTimeoutSec: 5, EnabledProviders: ids}
	for _, id := range ids {
		settings.Keys[id] = "rejected-key"
		settings.Config[id] = map[string]string{"endpoint": srv.URL}
	}
	search := func(id string) providerOutcome {
		return reg.Get(id).Search(context.Background(), settings, "systemd unit", 5)
	}

	if out := search(ids[0]); out.reason != string(ErrorCredentialRejected) || out.errorCode != "access_denied" {
		t.Fatalf("first call = %+v", out)
	}
	for _, id := range ids {
		if out := search(id); out.reason != string(ErrorCredentialRejected) {
			t.Fatalf("%s after rejection = %+v", id, out)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("calls after rejection = %d want 1", n)
	}
	if hosts := DeclaredEndpointHosts(settings, reg); slices.Contains(hosts, "example.com") {
		t.Fatalf("declared hosts include rejected providers: %v", hosts)
	}
	entry, _ := reg.Catalog().Entry(ids[1])
	if meta := providerMeta(entry, reg, settings, nil); meta.CredentialRejected == nil || meta.CredentialRejected.Code != "access_denied" {
		t.Fatalf("provider meta = %+v", meta.CredentialRejected)
	}

	changed := Settings{Keys: map[string]string{}, Config: settings.Config, PerProviderTimeoutSec: 5}
	for _, id := range ids {
		changed.Keys[id] = "replacement-key"
	}
	_ = reg.Get(ids[2]).Search(context.Background(), changed, "systemd unit", 5)
	if n := calls.Load(); n != 2 {
		t.Fatalf("calls after credential change = %d want 2", n)
	}

	Runtime{Catalog: reg.Catalog(), Registry: reg}.InvalidateCredential("stackexchange")
	if _, rejected := reg.credentialRejection(ids[0], changed); rejected {
		t.Fatal("invalidation kept the rejection")
	}
	_ = search(ids[3])
	if n := calls.Load(); n != 3 {
		t.Fatalf("calls after invalidation = %d want 3", n)
	}
}

func TestKeylessRejectionIsNotRecorded(t *testing.T) {
	var calls atomic.Int32
	srv := withProviderHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	})
	reg := testRegistry(t, srv.exchange)
	settings := Settings{Config: map[string]map[string]string{"searxng": {"endpoint": srv.URL}}, PerProviderTimeoutSec: 5}
	for i := 0; i < 2; i++ {
		if out := reg.Get("searxng").Search(context.Background(), settings, "query", 5); out.reason != string(ErrorRequestRejected) {
			t.Fatalf("keyless call %d = %+v", i, out)
		}
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("keyless calls = %d want 2", n)
	}
}
