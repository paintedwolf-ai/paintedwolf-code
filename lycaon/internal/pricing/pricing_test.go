package pricing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/modelfeed"
	"github.com/lycaon/lycaon/internal/testutil"
)

type failingRefreshModelFeed struct {
	doc *modelfeed.Document
	err error
}

func (f *failingRefreshModelFeed) Document(context.Context) (*modelfeed.Document, error) {
	return f.doc, nil
}

func (f *failingRefreshModelFeed) Refresh(context.Context) (*modelfeed.Document, error) {
	return nil, f.err
}

func (f *failingRefreshModelFeed) Snapshot() (*modelfeed.Document, string, bool) {
	return f.doc, string(StatusOK), f.doc != nil
}

func TestRateTableFromModelFeedFixture(t *testing.T) {
	body := readFixture(t, "models-dev", "api.json")
	table, err := parseModelFeedFixture(body)
	testutil.FailErr(t, "parseModelFeedFixture", err)
	rate, ok := table.Rates[RateKey{Kind: "openai", ModelID: "gpt-4.1"}]
	if !ok {
		t.Fatal("expected openai/gpt-4.1 rate")
	}
	assertRate(t, rate, 0.002, 0.008, 0.0005, 0.0025)
}

func TestModelsDevForceRefreshReportsFailureInsteadOfStaleSuccess(t *testing.T) {
	doc, err := modelfeed.ParseDocument(readFixture(t, "models-dev", "api.json"))
	testutil.FailErr(t, "ParseDocument", err)
	feedErr := errors.New("feed unavailable")
	source := newModelsDevSource(SourceConfig{
		ID: "models-dev", Kind: kindModelsDev, Label: "Models.dev",
	}, &failingRefreshModelFeed{doc: doc, err: feedErr}, time.Now)

	if _, err := source.Fetch(t.Context()); err != nil {
		testutil.FailErr(t, "ordinary cached fetch", err)
	}
	_, err = source.ForceRefresh(t.Context())
	if !errors.Is(err, ErrUnreachable) || !errors.Is(err, feedErr) {
		t.Fatalf("ForceRefresh error = %v, want unreachable feed failure", err)
	}
}

func TestParseLitellmFixture(t *testing.T) {
	body := readFixture(t, "litellm", "model_prices.json")
	table, err := ParseLitellm(body)
	testutil.FailErr(t, "ParseLitellm", err)
	rate, ok := table.Rates[RateKey{Kind: "openai", ModelID: "gpt-4.1"}]
	if !ok {
		t.Fatal("expected openai/gpt-4.1 rate")
	}
	assertRate(t, rate, 0.002, 0.008, 0.0005, 0.0025)
}

func TestParseAIPricingFYIFixture(t *testing.T) {
	body := readFixture(t, "ai-pricing-fyi", "current.json")
	table, err := parseAIPricingPages([][]byte{body})
	testutil.FailErr(t, "parseAIPricingPages", err)
	rate, ok := table.Rates[RateKey{Kind: "openai", ModelID: "gpt-4.1"}]
	if !ok {
		t.Fatal("expected openai/gpt-4.1 rate")
	}
	assertRate(t, rate, 0.002, 0.008, 0.0005, 0)
	want := time.Date(2025, 4, 15, 12, 0, 0, 0, time.UTC)
	if !table.SourceLastUpdated.Equal(want) {
		t.Fatalf("SourceLastUpdated = %v want %v", table.SourceLastUpdated, want)
	}
	if _, ok := table.Rates[RateKey{Kind: "openai", ModelID: "whisper"}]; ok {
		t.Fatal("non per_1m_tokens rows must be skipped")
	}
}

func TestAIPricingFYIPaginatesAndCombinesSplitModelMetrics(t *testing.T) {
	firstRows := make([]aiPricingRow, 0, aiPricingPageSize)
	for i := 0; i < aiPricingPageSize-1; i++ {
		firstRows = append(firstRows, aiPricingRow{
			ProviderSlug: "openai", ModelName: fmt.Sprintf("gpt-page-%d", i),
			Metric: "input_token", Unit: "per_1m_tokens", PriceNumeric: 1, Currency: "USD",
		})
	}
	firstRows = append(firstRows, aiPricingRow{
		ProviderSlug: "openai", ModelName: "gpt-split", Metric: "input_token",
		Unit: "per_1m_tokens", PriceNumeric: 2, Currency: "USD",
	})
	secondRows := []aiPricingRow{{
		ProviderSlug: "openai", ModelName: "gpt-split", Metric: "output_token",
		Unit: "per_1m_tokens", PriceNumeric: 8, Currency: "USD",
	}}
	firstBody, err := json.Marshal(aiPricingPage{Data: firstRows, Limit: aiPricingPageSize})
	testutil.FailErr(t, "marshal first page", err)
	secondBody, err := json.Marshal(aiPricingPage{Data: secondRows, Limit: aiPricingPageSize, Offset: aiPricingPageSize})
	testutil.FailErr(t, "marshal second page", err)
	fetcher := &seqFetcher{bodies: [][]byte{firstBody, secondBody}}
	cfg := SourcesConfig{Sources: []SourceConfig{{
		ID: "ai-pricing-fyi", Kind: kindAIPricingFYI, Label: "AI Pricing",
		URL: "https://example.com/v1/prices/current",
	}}}
	reg, err := NewRegistryFromConfig(t.Context(), cfg, RegistryOptions{CacheDir: t.TempDir(), GetBytes: fetcher.Get})
	testutil.FailErr(t, "NewRegistryFromConfig", err)
	table, err := reg.Refresh(context.Background(), "ai-pricing-fyi")
	testutil.FailErr(t, "Refresh", err)
	if len(fetcher.urls) != 2 {
		t.Fatalf("requests = %d, want 2", len(fetcher.urls))
	}
	if fetcher.urls[0] != "https://example.com/v1/prices/current?limit=100&offset=0" ||
		fetcher.urls[1] != "https://example.com/v1/prices/current?limit=100&offset=100" {
		t.Fatalf("request urls = %v", fetcher.urls)
	}
	rate, ok := table.Rates[RateKey{Kind: "openai", ModelID: "gpt-split"}]
	if !ok {
		t.Fatal("expected split model rate")
	}
	assertRate(t, rate, 0.002, 0.008, 0, 0)
}

func TestAIPricingFYIRejectsNonUSDCurrency(t *testing.T) {
	body := []byte(`{"data":[{"provider_slug":"openai","model_name":"gpt-4.1","metric":"input_token","unit":"per_1m_tokens","price_numeric":2,"currency":"EUR"}]}`)
	if _, err := parseAIPricingPages([][]byte{body}); err == nil {
		t.Fatal("expected unsupported currency rejection")
	}
}

func TestLitellmRejectsNegativeRate(t *testing.T) {
	body := []byte(`{"openai/gpt-4.1":{"litellm_provider":"openai","input_cost_per_token":-0.000002}}`)
	if _, err := ParseLitellm(body); err == nil {
		t.Fatal("expected negative rate rejection")
	}
}

func TestGuardedFetchBlocksLoopback(t *testing.T) {
	_, err := pricingFetch(RegistryOptions{Timeout: PricingFetchTimeout, MaxBytes: MaxPricingPayloadBytes})(t.Context(), "https://127.0.0.1/pricing.json")
	if err == nil || !errors.Is(err, ErrUnreachable) {
		t.Fatalf("want ErrUnreachable, got %v", err)
	}
}

func TestGuardedFetchBlocksMetadata(t *testing.T) {
	_, err := pricingFetch(RegistryOptions{Timeout: PricingFetchTimeout, MaxBytes: MaxPricingPayloadBytes})(t.Context(), "https://169.254.169.254/latest/meta-data/")
	if err == nil || !errors.Is(err, ErrUnreachable) {
		t.Fatalf("want ErrUnreachable, got %v", err)
	}
}

func TestGuardedFetchBlocksNonHTTPS(t *testing.T) {
	_, err := pricingFetch(RegistryOptions{Timeout: PricingFetchTimeout, MaxBytes: MaxPricingPayloadBytes})(t.Context(), "http://example.com/pricing.json")
	if err == nil {
		t.Fatal("want non-https rejection")
	}
}

func TestCachePreservedOnInvalidPayload(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	cfg := SourcesConfig{Sources: []SourceConfig{{
		ID: "litellm", Kind: kindLitellm, Label: "LiteLLM", URL: "https://example.com/prices.json",
	}}}
	good := readFixture(t, "litellm", "model_prices.json")
	fetcher := &seqFetcher{bodies: [][]byte{good, []byte("not-json{{{")}}
	reg, err := NewRegistryFromConfig(t.Context(), cfg, RegistryOptions{
		CacheDir: dir, GetBytes: fetcher.Get, Now: func() time.Time { return now },
	})
	testutil.FailErr(t, "NewRegistryFromConfig", err)

	table, err := reg.Refresh(context.Background(), "litellm")
	testutil.FailErr(t, "first Refresh", err)
	if table.Status != StatusOK {
		t.Fatalf("status = %s", table.Status)
	}
	cachedPath := cachePath(dir, "litellm")
	before, err := os.ReadFile(cachedPath)
	testutil.FailErr(t, "read cache", err)

	_, err = reg.Refresh(context.Background(), "litellm")
	if err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	after, err := os.ReadFile(cachedPath)
	testutil.FailErr(t, "re-read cache", err)
	if !bytes.Equal(before, after) {
		t.Fatal("invalid refresh must not overwrite disk cache")
	}
	cached, ok := reg.CachedTable("litellm")
	if !ok || cached.Status != StatusError {
		t.Fatalf("cached status = %v ok=%v", cached.Status, ok)
	}
	rate, ok := cached.Rates[RateKey{Kind: "openai", ModelID: "gpt-4.1"}]
	if !ok {
		t.Fatal("good rates must remain in memory cache")
	}
	assertRate(t, rate, 0.002, 0.008, 0.0005, 0.0025)
}

func TestShapeDriftedPayloadRejected(t *testing.T) {
	// Shape drift must not replace a usable cache.
	dir := t.TempDir()
	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	cfg := SourcesConfig{Sources: []SourceConfig{{
		ID: "litellm", Kind: kindLitellm, Label: "LiteLLM", URL: "https://example.com/prices.json",
	}}}
	good := readFixture(t, "litellm", "model_prices.json")
	drifted := []byte(`{"gpt-4.1":{"prompt_price":0.002,"completion_price":0.008,"litellm_provider":"openai"}}`)
	fetcher := &seqFetcher{bodies: [][]byte{good, drifted}}
	reg, err := NewRegistryFromConfig(t.Context(), cfg, RegistryOptions{
		CacheDir: dir, GetBytes: fetcher.Get, Now: func() time.Time { return now },
	})
	testutil.FailErr(t, "NewRegistryFromConfig", err)
	_, err = reg.Refresh(context.Background(), "litellm")
	testutil.FailErr(t, "first Refresh", err)
	before, err := os.ReadFile(cachePath(dir, "litellm"))
	testutil.FailErr(t, "read cache", err)

	_, err = reg.Refresh(context.Background(), "litellm")
	if err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid for zero-rate payload, got %v", err)
	}
	after, err := os.ReadFile(cachePath(dir, "litellm"))
	testutil.FailErr(t, "re-read cache", err)
	if !bytes.Equal(before, after) {
		t.Fatal("shape-drifted payload must leave cache intact")
	}
	cached, ok := reg.CachedTable("litellm")
	if !ok || cached.Status != StatusError {
		t.Fatalf("cached status = %v ok=%v", cached.Status, ok)
	}
	if _, ok := cached.Rates[RateKey{Kind: "openai", ModelID: "gpt-4.1"}]; !ok {
		t.Fatal("good rates must remain in memory")
	}
}

func TestCorruptDiskCacheDegradesNotBootFails(t *testing.T) {
	dir := t.TempDir()
	cfg := SourcesConfig{Sources: []SourceConfig{{
		ID: "litellm", Kind: kindLitellm, Label: "LiteLLM", URL: "https://example.com/prices.json",
	}}}
	testutil.FailErr(t, "seed corrupt cache", os.WriteFile(cachePath(dir, "litellm"), []byte("corrupt{{{"), 0o600))
	reg, err := NewRegistryFromConfig(t.Context(), cfg, RegistryOptions{CacheDir: dir, GetBytes: (&seqFetcher{}).Get})
	testutil.FailErr(t, "NewRegistryFromConfig must degrade on corrupt cache", err)
	if _, ok := reg.CachedTable("litellm"); ok {
		t.Fatal("corrupt cache must read as absent, not as a table")
	}
}

func TestOversizedPayloadRejected(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	cfg := SourcesConfig{Sources: []SourceConfig{{
		ID: "litellm", Kind: kindLitellm, Label: "LiteLLM", URL: "https://example.com/prices.json",
	}}}
	good := readFixture(t, "litellm", "model_prices.json")
	fetcher := &seqFetcher{bodies: [][]byte{good, nil}, errs: map[int]error{1: &httpclient.ResponseBodyTooLargeError{Limit: MaxPricingPayloadBytes}}}
	reg, err := NewRegistryFromConfig(t.Context(), cfg, RegistryOptions{
		CacheDir: dir, GetBytes: fetcher.Get, Now: func() time.Time { return now },
		MaxBytes: MaxPricingPayloadBytes,
	})
	testutil.FailErr(t, "NewRegistryFromConfig", err)
	_, err = reg.Refresh(context.Background(), "litellm")
	testutil.FailErr(t, "first Refresh", err)
	before, err := os.ReadFile(cachePath(dir, "litellm"))
	testutil.FailErr(t, "read cache", err)

	_, err = reg.Refresh(context.Background(), "litellm")
	if err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid for oversized payload, got %v", err)
	}
	after, err := os.ReadFile(cachePath(dir, "litellm"))
	testutil.FailErr(t, "re-read cache", err)
	if !bytes.Equal(before, after) {
		t.Fatal("oversized payload must leave cache intact")
	}
}

func TestModelsDevProjectsFromModelFeed(t *testing.T) {
	body := readFixture(t, "models-dev", "api.json")
	feed, err := newStubModelFeed(body)
	testutil.FailErr(t, "stub feed", err)
	cfg := SourcesConfig{Sources: []SourceConfig{{
		ID: "models-dev", Kind: kindModelsDev, Label: "Models.dev",
	}}}
	reg, err := NewRegistryFromConfig(t.Context(), cfg, RegistryOptions{
		CacheDir: t.TempDir(), ModelFeed: feed, GetBytes: (&seqFetcher{}).Get,
	})
	testutil.FailErr(t, "NewRegistryFromConfig", err)
	table, err := reg.Refresh(context.Background(), "models-dev")
	testutil.FailErr(t, "Refresh", err)
	rate, ok := table.Rates[RateKey{Kind: "openai", ModelID: "gpt-4.1"}]
	if !ok {
		t.Fatalf("rates = %+v", table.Rates)
	}
	assertRate(t, rate, 0.002, 0.008, 0.0005, 0.0025)
}

func TestFireworksKindIdentity(t *testing.T) {
	body := []byte(`{
  "fireworks-ai": {
    "id": "fireworks-ai",
    "models": {
      "accounts/fireworks/models/kimi-k2p7-code": {
        "id": "accounts/fireworks/models/kimi-k2p7-code",
        "cost": {"input": 0.95, "output": 4.0}
      }
    }
  }
}`)
	table, err := parseModelFeedFixture(body)
	testutil.FailErr(t, "parseModelFeedFixture", err)
	rate, ok := table.Rates[RateKey{Kind: "fireworks", ModelID: "accounts/fireworks/models/kimi-k2p7-code"}]
	if !ok {
		t.Fatalf("want fireworks kind key, rates=%+v", table.Rates)
	}
	assertRate(t, rate, 0.00095, 0.004, 0, 0)

	litellm := []byte(`{
  "fireworks_ai/accounts/fireworks/models/kimi-k2p7-code": {
    "litellm_provider": "fireworks_ai",
    "input_cost_per_token": 0.00000095,
    "output_cost_per_token": 0.000004
  }
}`)
	lt, err := ParseLitellm(litellm)
	testutil.FailErr(t, "ParseLitellm", err)
	rate, ok = lt.Rates[RateKey{Kind: "fireworks", ModelID: "accounts/fireworks/models/kimi-k2p7-code"}]
	if !ok {
		t.Fatalf("want stripped litellm key, rates=%+v", lt.Rates)
	}
	assertRate(t, rate, 0.00095, 0.004, 0, 0)
}

func TestUnknownKindRejected(t *testing.T) {
	_, err := ParseSourcesConfig([]byte(`
sources:
  - id: bad
    kind: local-catalog
    label: Bad
    url: "https://example.com/x.json"
`))
	if err == nil || !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("want ErrUnknownKind, got %v", err)
	}
}

func TestRegistryUsesCatalogKernelWithoutLosingPricingPriority(t *testing.T) {
	cfg := SourcesConfig{Sources: []SourceConfig{
		{ID: "z-feed", Kind: kindLitellm, URL: "https://example.com/z.json"},
		{ID: "a-feed", Kind: kindLitellm, URL: "https://example.com/a.json"},
	}}
	reg, err := NewRegistryFromConfig(t.Context(), cfg, RegistryOptions{
		CacheDir: t.TempDir(), GetBytes: (&seqFetcher{}).Get,
	})
	testutil.FailErr(t, "NewRegistryFromConfig", err)
	got := reg.List()
	if len(got) != 2 || got[0].ID() != "z-feed" || got[1].ID() != "a-feed" {
		t.Fatalf("registry order = %+v, want pricing priority z-feed then a-feed", got)
	}

	_, err = NewRegistryFromConfig(t.Context(), SourcesConfig{Sources: []SourceConfig{
		{ID: "same", Kind: kindLitellm, URL: "https://example.com/one.json"},
		{ID: "same", Kind: kindLitellm, URL: "https://example.com/two.json"},
	}}, RegistryOptions{CacheDir: t.TempDir(), GetBytes: (&seqFetcher{}).Get})
	if err == nil || !strings.Contains(err.Error(), "duplicate source id") {
		t.Fatalf("duplicate registry config error = %v", err)
	}
}

func TestLoadBundledPricingSources(t *testing.T) {
	cfg, err := LoadSourcesConfig()
	testutil.FailErr(t, "LoadSourcesConfig", err)
	if len(cfg.Sources) != 3 {
		t.Fatalf("want 3 sources, got %d", len(cfg.Sources))
	}
	feed, err := newStubModelFeed(readFixture(t, "models-dev", "api.json"))
	testutil.FailErr(t, "stub feed", err)
	reg, err := NewRegistryFromConfig(t.Context(), cfg, RegistryOptions{
		CacheDir:  t.TempDir(),
		GetBytes:  (&seqFetcher{}).Get, // no network for HTTP feeds
		ModelFeed: feed,
	})
	testutil.FailErr(t, "NewRegistryFromConfig", err)
	if len(reg.List()) != 3 {
		t.Fatalf("want 3 registered, got %d", len(reg.List()))
	}
	src, ok := reg.Get("models-dev")
	if !ok || src.URL() != "" {
		t.Fatalf("models-dev must have empty URL, got %+v", src)
	}
}

func assertRate(t *testing.T, got Rate, in, out, cr, cw float64) {
	t.Helper()
	if got.Currency != "USD" {
		t.Fatalf("currency = %q", got.Currency)
	}
	const eps = 1e-12
	if abs(rateValue(got.InputPer1K)-in) > eps || abs(rateValue(got.OutputPer1K)-out) > eps ||
		abs(rateValue(got.CacheReadPer1K)-cr) > eps || abs(rateValue(got.CacheWritePer1K)-cw) > eps {
		t.Fatalf("rate = %+v want in=%v out=%v cr=%v cw=%v", got, in, out, cr, cw)
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func readFixture(t *testing.T, kind, name string) []byte {
	t.Helper()
	p := filepath.Join("testdata", kind, name)
	body, err := os.ReadFile(p)
	testutil.FailErr(t, "read fixture "+p, err)
	return body
}

type seqFetcher struct {
	bodies [][]byte
	errs   map[int]error
	i      int
	urls   []string
}

func (s *seqFetcher) Get(_ context.Context, rawURL string) ([]byte, error) {
	if s.i >= len(s.bodies) {
		return nil, errors.New("no more feed results")
	}
	body, err := s.bodies[s.i], s.errs[s.i]
	s.i++
	s.urls = append(s.urls, rawURL)
	return body, err
}

type stubModelFeed struct {
	doc *modelfeed.Document
}

func newStubModelFeed(body []byte) (*stubModelFeed, error) {
	doc, err := modelfeed.ParseDocument(body)
	if err != nil {
		return nil, err
	}
	doc.FetchedAt = time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	return &stubModelFeed{doc: doc}, nil
}

func (s *stubModelFeed) Document(context.Context) (*modelfeed.Document, error) {
	return s.doc, nil
}

func (s *stubModelFeed) Refresh(context.Context) (*modelfeed.Document, error) {
	return s.doc, nil
}

func (s *stubModelFeed) Snapshot() (*modelfeed.Document, string, bool) {
	return s.doc, string(StatusOK), s.doc != nil
}

func rateValue(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func parseModelFeedFixture(body []byte) (RateTable, error) {
	doc, err := modelfeed.ParseDocument(body)
	if err != nil {
		return RateTable{}, err
	}
	table := RateTableFromDocument(doc)
	return table, validateRateTable(table)
}
