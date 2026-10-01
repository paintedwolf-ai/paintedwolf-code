package webresearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// Synthetic access-token canary.
const plantedAWS = "AKIAQYJK5TXV4NZR7SGB"
const wantAWSRule = "gitleaks:aws-access-token"

func testSecretMatcher(t *testing.T) *secretmatch.Matcher {
	t.Helper()
	m, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "BuildMatcher secret-patterns", err)
	fingerprinter, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("w", 32)))
	testutil.FailErr(t, "build fingerprinter", err)
	m.SetFingerprinter(fingerprinter)
	return m
}

func googleCSETestSettings() Settings {
	return Settings{
		Keys:                  map[string]string{"google_cse": "secret"},
		Config:                map[string]map[string]string{"google_cse": {"search_engine_id": "cx123"}},
		PerProviderTimeoutSec: 5,
	}
}

func TestWebSearchScreenAsksAndDenies(t *testing.T) {
	var hits atomic.Int32
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
	})
	spec := googleCseRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?q=" + url.QueryEscape(query), nil
	}
	p := NewRESTSearchProvider(spec, KindKeyedExtra)

	var askCalls atomic.Int32
	var lastFinding secretmatch.Alert
	ask := func(_ context.Context, finding secretmatch.Alert) (secretmatch.Resolution, error) {
		askCalls.Add(1)
		lastFinding = finding
		if finding.Surface != secretmatch.SurfaceWebSearch {
			t.Errorf("surface=%s", finding.Surface)
		}
		return secretmatch.Resolution{Decision: secretmatch.Unanswered}, nil
	}
	ctx := withSecretScreen(context.Background(), testSecretMatcher(t), ask, "", screenDestination{})
	out := p.Search(ctx, googleCSETestSettings(), "leak "+plantedAWS, 5)
	if out.ok || out.reason != "secret_denied" {
		t.Fatalf("outcome=%+v want secret_denied", out)
	}
	if hits.Load() != 0 {
		t.Fatalf("provider HTTP called %d times", hits.Load())
	}
	if askCalls.Load() != 1 {
		t.Fatalf("ask calls=%d", askCalls.Load())
	}
	if lastFinding.RuleID != wantAWSRule || !lastFinding.Surface.CanRedact() || lastFinding.SourcePath != "query" ||
		lastFinding.OriginKind != secretmatch.OriginField {
		t.Fatalf("finding=%+v", lastFinding)
	}
	if len(lastFinding.Fingerprints) != 1 {
		t.Fatalf("fingerprints = %v, want one exact secret identity", lastFinding.Fingerprints)
	}
	denied, ok := SecretScreenDenied(ctx)
	if !ok {
		t.Fatal("expected SecretScreenDenied")
	}
	rej := secretScreenRejectFromErr(denied)
	if rej.Code != "OUTBOUND_SECRET_DENIED" {
		t.Fatalf("code=%s", rej.Code)
	}
	shape, _ := rej.Data["shape"].(string)
	if len(rej.Data) != 4 || shape == "" {
		t.Fatalf("reject data=%#v", rej.Data)
	}
	assertNoSecretValue(t, plantedAWS, denied.Match, rej)
}

func TestWebSearchScreenApproveSendsVerbatim(t *testing.T) {
	query := "docs about " + plantedAWS
	var gotQ string
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		gotQ = r.URL.Query().Get("q")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]string{{"title": "Hit", "link": "https://example.com/", "snippet": "s"}},
		})
	})
	spec := googleCseRESTSpec()
	spec.BuildURL = func(s Settings, q string, max int) (string, error) {
		return srv.URL + "?q=" + url.QueryEscape(q), nil
	}
	p := NewRESTSearchProvider(spec, KindKeyedExtra)
	ask := func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
		return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
	}
	ctx := withSecretScreen(context.Background(), testSecretMatcher(t), ask, "", screenDestination{})
	out := p.Search(ctx, googleCSETestSettings(), query, 5)
	if !out.ok {
		t.Fatalf("outcome=%+v", out)
	}
	if gotQ != query {
		t.Fatalf("recorded query %q want verbatim %q", gotQ, query)
	}
}

func TestWebSearchScreenRedactsTransientQuery(t *testing.T) {
	query := "docs about " + plantedAWS
	var gotQ string
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		gotQ = r.URL.Query().Get("q")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
	})
	spec := googleCseRESTSpec()
	spec.BuildURL = func(_ Settings, q string, _ int) (string, error) {
		return srv.URL + "?q=" + url.QueryEscape(q), nil
	}
	p := NewRESTSearchProvider(spec, KindKeyedExtra)
	ctx := withSecretScreen(context.Background(), testSecretMatcher(t), func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
		return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
	}, "", screenDestination{})
	out := p.Search(ctx, googleCSETestSettings(), query, 5)
	if !out.ok {
		t.Fatalf("outcome=%+v", out)
	}
	if strings.Contains(gotQ, plantedAWS) || !strings.Contains(gotQ, "[REDACTED]") {
		t.Fatalf("screened query = %q", gotQ)
	}
	if !strings.Contains(query, plantedAWS) {
		t.Fatal("screen mutated original query")
	}
}

func TestFetchURLScreenDeniesBeforeDial(t *testing.T) {
	var dialAsks atomic.Int32
	confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		dialAsks.Add(1)
		return true
	})
	t.Cleanup(func() { confine.SetEgressResolver(nil) })
	confine.SetEgressPosture(confine.PostureAsk)
	t.Cleanup(func() { confine.SetEgressPosture(confine.PostureObserve) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("HTTP dialed after secret deny")
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	allowLoopbackFetch(t)

	u, err := url.Parse(srv.URL + "/?key=" + plantedAWS)
	testutil.FailErr(t, "parse", err)

	ask := func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
		return secretmatch.Resolution{Decision: secretmatch.Unanswered}, nil
	}
	ctx := egressgate.WithAttribution(context.Background(), confine.EgressCommand{SessionID: "sess-secret-fetch"})
	ctx = withSecretScreen(ctx, testSecretMatcher(t), ask, "", screenDestination{})

	resp, _, err := guardedGet(ctx, u, "")
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	var denied *SecretDeniedError
	if !errorsAsSecret(err, &denied) {
		t.Fatalf("want SecretDeniedError, got %v", err)
	}
	if denied.Match.RuleID != wantAWSRule {
		t.Fatalf("rule_id=%q want %s", denied.Match.RuleID, wantAWSRule)
	}
	if dialAsks.Load() != 0 {
		t.Fatalf("host gate ran %d times", dialAsks.Load())
	}
	assertNoSecretValue(t, plantedAWS, denied.Match, secretScreenRejectFromErr(denied))
}

func TestFetchURLScreenRedactsTransientURL(t *testing.T) {
	original := "https://example.com/docs?key=" + plantedAWS
	u, err := url.Parse(original)
	testutil.FailErr(t, "parse URL", err)
	destination := httpDestination(u)
	ctx := withSecretScreen(context.Background(), testSecretMatcher(t), func(_ context.Context, finding secretmatch.Alert) (secretmatch.Resolution, error) {
		if finding.Surface != secretmatch.SurfaceFetchURL || finding.SourcePath != "url" || finding.DestinationID != destination.id {
			t.Errorf("finding = %+v", finding)
		}
		return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
	}, "", screenDestination{})
	got, err := screenOutbound(ctx, secretmatch.SurfaceFetchURL, destination, original)
	testutil.FailErr(t, "screen fetch URL", err)
	if strings.Contains(got, plantedAWS) || !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("screened URL = %q", got)
	}
	if _, err := normalizeFetchURL(got); err != nil {
		t.Fatalf("redacted URL is invalid: %v", err)
	}
	if !strings.Contains(original, plantedAWS) {
		t.Fatal("screen mutated original URL")
	}
}

func TestScreenInertMatcherByteIdentical(t *testing.T) {
	var hits atomic.Int32
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]string{{"title": "Hit", "link": "https://example.com/", "snippet": "s"}},
		})
	})
	spec := googleCseRESTSpec()
	spec.BuildURL = func(s Settings, query string, max int) (string, error) {
		return srv.URL + "?q=" + url.QueryEscape(query), nil
	}
	p := NewRESTSearchProvider(spec, KindKeyedExtra)

	var askCalls atomic.Int32
	ask := func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
		askCalls.Add(1)
		return secretmatch.Resolution{Decision: secretmatch.Unanswered}, nil
	}
	ctx := withSecretScreen(context.Background(), nil, ask, "", screenDestination{})
	out := p.Search(ctx, googleCSETestSettings(), "leak "+plantedAWS, 5)
	if !out.ok {
		t.Fatalf("nil matcher must not reject: %+v", out)
	}
	if askCalls.Load() != 0 || hits.Load() != 1 {
		t.Fatalf("ask=%d hits=%d", askCalls.Load(), hits.Load())
	}

	ctx = withSecretScreen(context.Background(), secretmatch.NewInertMatcher(), ask, "", screenDestination{})
	out = p.Search(ctx, googleCSETestSettings(), "leak "+plantedAWS, 5)
	if !out.ok || askCalls.Load() != 0 {
		t.Fatalf("inert matcher must be byte-identical: out=%+v ask=%d", out, askCalls.Load())
	}
}

func TestScreenOneAskPerInvocation(t *testing.T) {
	query := plantedAWS + " and " + plantedAWS
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
	})
	spec := googleCseRESTSpec()
	spec.BuildURL = func(s Settings, q string, max int) (string, error) {
		return srv.URL + "?q=" + url.QueryEscape(q), nil
	}
	p := NewRESTSearchProvider(spec, KindKeyedExtra)
	var askCalls atomic.Int32
	ask := func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
		askCalls.Add(1)
		return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
	}
	ctx := withSecretScreen(context.Background(), testSecretMatcher(t), ask, "", screenDestination{})
	_ = p.Search(ctx, googleCSETestSettings(), query, 5)
	_ = p.Search(ctx, googleCSETestSettings(), query, 5)
	if askCalls.Load() != 1 {
		t.Fatalf("ask calls=%d want 1", askCalls.Load())
	}
}

func TestScreenPayloadsNeverCarryValue(t *testing.T) {
	ask := func(_ context.Context, finding secretmatch.Alert) (secretmatch.Resolution, error) {
		m := secretmatch.Match{RuleID: finding.RuleID, Title: finding.RuleTitle, GenericShape: finding.GenericShape}
		assertNoSecretValue(t, plantedAWS, m, secretScreenReject(finding.Surface, finding.DestinationID, m, ""))
		return secretmatch.Resolution{Decision: secretmatch.Unanswered}, nil
	}
	ctx := withSecretScreen(context.Background(), testSecretMatcher(t), ask, "", screenDestination{})
	spec := googleCseRESTSpec()
	spec.BuildURL = func(Settings, string, int) (string, error) {
		t.Fatal("BuildURL must not run on deny")
		return "", nil
	}
	p := NewRESTSearchProvider(spec, KindKeyedExtra)
	_ = p.Search(ctx, googleCSETestSettings(), plantedAWS, 5)
}

func assertNoSecretValue(t *testing.T, value string, m secretmatch.Match, rej *tools.ToolReject) {
	t.Helper()
	blob, err := json.Marshal(map[string]any{
		"match": m,
		"rej":   rej,
		"data":  rej.Data,
	})
	testutil.FailErr(t, "marshal", err)
	if strings.Contains(string(blob), value) {
		t.Fatalf("secret value leaked into payload: %s", blob)
	}
	if strings.Contains(m.GenericShape, value) {
		t.Fatalf("shape contains value: %q", m.GenericShape)
	}
}

func errorsAsSecret(err error, target **SecretDeniedError) bool {
	return errors.As(err, target)
}

// Every non-send decision blocks the outbound value.
func TestScreenOutboundBlocksEveryNonSendDecision(t *testing.T) {
	original := "https://example.com/docs?key=" + plantedAWS
	u, err := url.Parse(original)
	testutil.FailErr(t, "parse URL", err)
	destination := httpDestination(u)
	for _, decision := range []secretmatch.Decision{secretmatch.Unanswered, secretmatch.Withhold} {
		t.Run(string(decision), func(t *testing.T) {
			ctx := withSecretScreen(context.Background(), testSecretMatcher(t), func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
				return secretmatch.Resolution{Decision: decision, Guidance: "use the public endpoint"}, nil
			}, "", screenDestination{})
			got, err := screenOutbound(ctx, secretmatch.SurfaceFetchURL, destination, original)
			var denied *SecretDeniedError
			if !errorsAsSecret(err, &denied) {
				t.Fatalf("want SecretDeniedError, got err=%v out=%q", err, got)
			}
			if strings.Contains(got, plantedAWS) {
				t.Fatalf("blocked screen returned the value: %q", got)
			}
			reject := secretScreenRejectFromErr(denied)
			if reject.Data[tools.UserGuidanceKey] != "use the public endpoint" {
				t.Fatalf("reject dropped the user's direction: %+v", reject.Data)
			}
			assertNoSecretValue(t, plantedAWS, denied.Match, reject)
		})
	}
}
