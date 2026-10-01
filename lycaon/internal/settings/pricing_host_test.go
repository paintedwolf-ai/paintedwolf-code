package settings_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/cost/costtest"
	"github.com/lycaon/lycaon/internal/modelfeed"
	"github.com/lycaon/lycaon/internal/pricing"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type countingFetcher struct {
	n atomic.Int32
}

func (c *countingFetcher) Get(context.Context, string) ([]byte, error) {
	c.n.Add(1)
	return nil, nil
}

type hostStubFeed struct {
	doc *modelfeed.Document
}

func newHostStubFeed(t *testing.T) *hostStubFeed {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(configlayout.FindModuleRoot(), "internal", "pricing", "testdata", "models-dev", "api.json"))
	testutil.FailErr(t, "read fixture", err)
	doc, err := modelfeed.ParseDocument(body)
	testutil.FailErr(t, "ParseDocument", err)
	doc.FetchedAt = time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	return &hostStubFeed{doc: doc}
}

func (s *hostStubFeed) Document(context.Context) (*modelfeed.Document, error) {
	return s.doc, nil
}
func (s *hostStubFeed) Refresh(context.Context) (*modelfeed.Document, error) {
	return s.doc, nil
}
func (s *hostStubFeed) Snapshot() (*modelfeed.Document, string, bool) {
	return s.doc, string(pricing.StatusOK), s.doc != nil
}

type stubKinds struct{}

func (stubKinds) PricedAsModelID(_, model string) string { return model }

func (stubKinds) LocalFree(instanceID string) bool { return instanceID == "local-1" }

func (stubKinds) ProviderKind(instanceID string) (string, bool) {
	switch instanceID {
	case "fireworks-1":
		return "fireworks", true
	case "openai", "openai-1":
		return "openai", true
	default:
		return "", false
	}
}

func TestPricingHostKillSwitchQuiet(t *testing.T) {
	store, err := settings.NewPricingStoreAt(filepath.Join(t.TempDir(), "pricing.yaml"))
	testutil.FailErr(t, "NewPricingStoreAt", err)
	cfg, err := pricing.LoadSourcesConfig()
	testutil.FailErr(t, "LoadSourcesConfig", err)

	fetcher := &countingFetcher{}
	tracker := costtest.NewTracker(t, cost.NoopPricer{})
	host := &settings.PricingHost{
		Store:     store,
		Catalog:   cfg,
		CacheDir:  t.TempDir(),
		Tracker:   tracker,
		GetBytes:  fetcher.Get,
		ModelFeed: newHostStubFeed(t),
		Kinds:     stubKinds{},
	}

	off := false
	testutil.FailErr(t, "PutGlobal off", store.PutGlobal(settings.PricingUserOverlay{
		CostTrackingEnabled: &off,
		Sources: []settings.PricingSourcePref{
			{ID: "models-dev", Enabled: true},
			{ID: "litellm", Enabled: false},
			{ID: "ai-pricing-fyi", Enabled: false},
		},
	}))
	testutil.FailErr(t, "SyncFromStore off", host.SyncFromStore(context.Background()))
	if fetcher.n.Load() != 0 {
		t.Fatalf("tracking off must not fetch; got %d", fetcher.n.Load())
	}
	testutil.FailErr(t, "begin local call while tracking is off", tracker.BeginCall(t.Context(), cost.UsageEvent{
		ID: "local-off", SessionID: "session-local", ProviderID: "local-1", Model: "local-model",
	}))
	testutil.FailErr(t, "mark local call unknown", tracker.MarkCallUnknown(t.Context(), "local-off"))
	localSummary, err := tracker.Summary(t.Context(), "session", "session-local", "")
	testutil.FailErr(t, "summarize local call", err)
	if localSummary.UnknownCalls != 1 || localSummary.UnknownChargedCalls != 0 {
		t.Fatalf("tracking-off local classification = %+v, want one no-charge unknown call", localSummary)
	}

	on := true
	testutil.FailErr(t, "PutGlobal on", store.PutGlobal(settings.PricingUserOverlay{
		CostTrackingEnabled: &on,
		Sources: []settings.PricingSourcePref{
			{ID: "models-dev", Enabled: true},
			{ID: "litellm", Enabled: false},
			{ID: "ai-pricing-fyi", Enabled: false},
		},
	}))
	testutil.FailErr(t, "SyncFromStore on", host.SyncFromStore(context.Background()))
	waitPricingSettled(t, host)
	if fetcher.n.Load() != 0 {
		t.Fatalf("models-dev must project from modelfeed without HTTP; got %d", fetcher.n.Load())
	}

	est, err := tracker.Estimate(context.Background(), "openai-1", "gpt-4.1", cost.TokenUsage{
		PromptTokens: 1000, CompletionTokens: 1000,
	})
	testutil.FailErr(t, "Estimate", err)
	if est.Unpriced || est.PricingSource != "models-dev" {
		t.Fatalf("est = %+v", est)
	}
}

func TestPricingHostRejectsRefreshForDisabledSourceWithoutFetching(t *testing.T) {
	store, err := settings.NewPricingStoreAt(filepath.Join(t.TempDir(), "pricing.yaml"))
	testutil.FailErr(t, "NewPricingStoreAt", err)
	cfg, err := pricing.LoadSourcesConfig()
	testutil.FailErr(t, "LoadSourcesConfig", err)
	on := true
	testutil.FailErr(t, "PutGlobal", store.PutGlobal(settings.PricingUserOverlay{
		CostTrackingEnabled: &on,
		Sources: []settings.PricingSourcePref{
			{ID: "models-dev", Enabled: true},
			{ID: "litellm", Enabled: false},
			{ID: "ai-pricing-fyi", Enabled: false},
		},
	}))
	fetcher := &countingFetcher{}
	host := &settings.PricingHost{
		Store: store, Catalog: cfg, CacheDir: t.TempDir(),
		Tracker: costtest.NewTracker(t, cost.NoopPricer{}), GetBytes: fetcher.Get,
		ModelFeed: newHostStubFeed(t), Kinds: stubKinds{},
	}
	_, err = host.RefreshSource(context.Background(), "litellm")
	if !errors.Is(err, settings.ErrPricingSourceDisabled) {
		t.Fatalf("error = %v, want ErrPricingSourceDisabled", err)
	}
	if fetcher.n.Load() != 0 {
		t.Fatalf("disabled refresh must not fetch; got %d requests", fetcher.n.Load())
	}
}

func TestPricingHostRefreshErrorsStayStructured(t *testing.T) {
	store, err := settings.NewPricingStoreAt(filepath.Join(t.TempDir(), "pricing.yaml"))
	testutil.FailErr(t, "NewPricingStoreAt", err)
	cfg, err := pricing.LoadSourcesConfig()
	testutil.FailErr(t, "LoadSourcesConfig", err)
	off := false
	testutil.FailErr(t, "PutGlobal", store.PutGlobal(settings.PricingUserOverlay{
		CostTrackingEnabled: &off,
		Sources:             []settings.PricingSourcePref{{ID: "models-dev", Enabled: true}},
	}))
	host := &settings.PricingHost{Store: store, Catalog: cfg, Tracker: costtest.NewTracker(t, cost.NoopPricer{})}

	_, err = host.RefreshSource(t.Context(), "models-dev")
	if !errors.Is(err, settings.ErrCostTrackingDisabled) {
		t.Fatalf("disabled tracking error = %v", err)
	}
	if code := settings.RefreshErrorCode(err); code != wire.ApiErrorCodePricingSourceDisabled {
		t.Fatalf("mapped disabled error = %q", code)
	}

	_, err = host.RefreshSource(t.Context(), "missing")
	if !errors.Is(err, pricing.ErrUnknownID) {
		t.Fatalf("unknown source error = %v", err)
	}
}

func TestPricingHostAutoRefreshRetriesFailedSource(t *testing.T) {
	store, err := settings.NewPricingStoreAt(filepath.Join(t.TempDir(), "pricing.yaml"))
	testutil.FailErr(t, "NewPricingStoreAt", err)
	cfg, err := pricing.LoadSourcesConfig()
	testutil.FailErr(t, "LoadSourcesConfig", err)

	// Auto-refresh retries a source with an invalid cached response.
	good, err := os.ReadFile(filepath.Join(configlayout.FindModuleRoot(), "internal", "pricing", "testdata", "litellm", "model_prices.json"))
	testutil.FailErr(t, "read litellm fixture", err)
	fetcher := &flakyFetcher{bodies: [][]byte{[]byte("not-json{{{"), good}}
	tracker := costtest.NewTracker(t, cost.NoopPricer{})
	host := &settings.PricingHost{
		Store:           store,
		Catalog:         cfg,
		CacheDir:        t.TempDir(),
		Tracker:         tracker,
		GetBytes:        fetcher.Get,
		ModelFeed:       newHostStubFeed(t),
		Kinds:           stubKinds{},
		RefreshInterval: 20 * time.Millisecond,
	}

	on := true
	testutil.FailErr(t, "PutGlobal on", store.PutGlobal(settings.PricingUserOverlay{
		CostTrackingEnabled: &on,
		Sources: []settings.PricingSourcePref{
			{ID: "models-dev", Enabled: false},
			{ID: "litellm", Enabled: true},
			{ID: "ai-pricing-fyi", Enabled: false},
		},
	}))
	testutil.FailErr(t, "SyncFromStore", host.SyncFromStore(context.Background()))
	waitPricingSettled(t, host)
	if status := pricingSourceStatus(host, "litellm"); status != string(pricing.StatusError) {
		t.Fatalf("first fetch status = %q, want error", status)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host.StartAutoRefresh(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for pricingSourceStatus(host, "litellm") != string(pricing.StatusOK) {

		if time.Now().After(deadline) {
			t.Fatalf("litellm never recovered; status = %q", pricingSourceStatus(host, "litellm"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPricingHostApplySettingsDoesNotWaitForFetch(t *testing.T) {
	store, err := settings.NewPricingStoreAt(filepath.Join(t.TempDir(), "pricing.yaml"))
	testutil.FailErr(t, "NewPricingStoreAt", err)
	cfg, err := pricing.LoadSourcesConfig()
	testutil.FailErr(t, "LoadSourcesConfig", err)
	good, err := os.ReadFile(filepath.Join(configlayout.FindModuleRoot(), "internal", "pricing", "testdata", "litellm", "model_prices.json"))
	testutil.FailErr(t, "read litellm fixture", err)

	fetcher := &gatedFetcher{release: make(chan struct{}), body: good}
	tracker := costtest.NewTracker(t, cost.NoopPricer{})
	host := &settings.PricingHost{
		Store: store, Catalog: cfg, CacheDir: t.TempDir(),
		Tracker: tracker, GetBytes: fetcher.Get,
		ModelFeed: newHostStubFeed(t), Kinds: stubKinds{},
	}
	var settled atomic.Int32
	host.SetOnRefreshSettled(func() { settled.Add(1) })

	on := true
	done := make(chan error, 1)
	go func() {
		done <- host.ApplySettings(t.Context(), settings.PricingUserOverlay{
			CostTrackingEnabled: &on,
			Sources: []settings.PricingSourcePref{
				{ID: "models-dev", Enabled: false},
				{ID: "litellm", Enabled: true},
				{ID: "ai-pricing-fyi", Enabled: false},
			},
		})
	}()
	select {
	case err := <-done:
		testutil.FailErr(t, "ApplySettings", err)
	case <-time.After(5 * time.Second):
		close(fetcher.release)
		t.Fatal("ApplySettings waited on the feed fetch")
	}

	meta := pricingSourceMeta(host, "litellm")
	if !meta.Refreshing || !meta.Enabled {
		t.Fatalf("litellm meta while fetch blocked = %+v, want enabled and refreshing", meta)
	}
	if settled.Load() != 0 {
		t.Fatalf("settled callback ran before the fetch finished")
	}

	close(fetcher.release)
	waitPricingSettled(t, host)
	if status := pricingSourceStatus(host, "litellm"); status != string(pricing.StatusOK) {
		t.Fatalf("litellm status after fetch = %q, want ok", status)
	}
	if settled.Load() != 1 {
		t.Fatalf("settled callback count = %d, want 1", settled.Load())
	}
	est, err := tracker.Estimate(t.Context(), "openai-1", "gpt-4.1", cost.TokenUsage{PromptTokens: 1000, CompletionTokens: 1000})
	testutil.FailErr(t, "Estimate", err)
	if est.Unpriced || est.PricingSource != "litellm" {
		t.Fatalf("estimate after background fetch = %+v, want priced by litellm", est)
	}
}

// gatedFetcher holds every request until release closes.
type gatedFetcher struct {
	release chan struct{}
	body    []byte
}

func (g *gatedFetcher) Get(ctx context.Context, _ string) ([]byte, error) {
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return g.body, nil
}

func waitPricingSettled(t *testing.T, host *settings.PricingHost) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		busy := false
		for _, m := range host.AvailableSources() {
			busy = busy || m.Refreshing
		}
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("pricing fetches never settled")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func pricingSourceMeta(host *settings.PricingHost, id string) wire.PricingSourceMeta {
	for _, m := range host.AvailableSources() {
		if m.ID == id {
			return m
		}
	}
	return wire.PricingSourceMeta{}
}

func pricingSourceStatus(host *settings.PricingHost, id string) string {
	for _, m := range host.AvailableSources() {
		if m.ID == id {
			return string(m.Status)
		}
	}
	return ""
}

// flakyFetcher repeats its final response.
type flakyFetcher struct {
	mu     sync.Mutex
	bodies [][]byte
	calls  int
}

func (f *flakyFetcher) Get(context.Context, string) ([]byte, error) {
	f.mu.Lock()
	idx := f.calls
	if idx >= len(f.bodies) {
		idx = len(f.bodies) - 1
	}
	body := f.bodies[idx]
	f.calls++
	f.mu.Unlock()
	return body, nil
}
