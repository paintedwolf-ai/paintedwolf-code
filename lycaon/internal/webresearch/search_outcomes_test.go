package webresearch

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestFirstAttemptWaitsForPacingSlotAndRetryDoesNot(t *testing.T) {
	gate := newProviderGate("p", &PacingSpec{MinIntervalMS: 50}, nil, nil)
	first := withProviderGateWait(context.Background(), firstAttemptGateWait)
	if err := gate.acquire(first, false); err != nil {
		t.Fatalf("first slot: %v", err)
	}
	if err := gate.acquire(withProviderGateWait(context.Background(), 0), false); !errors.Is(err, ErrProviderPaced) {
		t.Fatalf("retry with no free slot = %v, want paced", err)
	}
	start := time.Now()
	if err := gate.acquire(first, false); err != nil {
		t.Fatalf("sibling first attempt within the wait: %v", err)
	}
	if time.Since(start) <= 0 {
		t.Fatal("sibling first attempt did not wait for its slot")
	}
}

type gateWaitRecorder struct {
	waits []time.Duration
}

func (r *gateWaitRecorder) ID() string               { return "rec" }
func (r *gateWaitRecorder) Kind() ProviderKind       { return ProviderKind("") }
func (r *gateWaitRecorder) Configured(Settings) bool { return true }
func (r *gateWaitRecorder) Search(ctx context.Context, _ Settings, _ string, _ int) providerOutcome {
	r.waits = append(r.waits, providerGateWaitFrom(ctx))
	return providerOutcome{providerID: "rec", ok: true}
}

func TestBackoffRetriesTakeOnlyFreeSlots(t *testing.T) {
	rec := &gateWaitRecorder{}
	p := &queryBackoffProvider{inner: rec, minWords: 1}
	ctx := withProviderGateWait(context.Background(), firstAttemptGateWait)
	p.Search(ctx, Settings{PerProviderTimeoutSec: 8}, "one two three", 5)
	if !slices.Equal(rec.waits, []time.Duration{firstAttemptGateWait, 0, 0}) {
		t.Fatalf("gate waits = %v", rec.waits)
	}
}

func TestFinalizeSearchResultUsesPrimaryOutcomes(t *testing.T) {
	soft := map[string]struct{}{"fb1": {}, "fb2": {}}
	hit := WebHit{URL: "https://example.com/a", Title: "a"}
	for _, tc := range []struct {
		name          string
		outcomes      []providerOutcome
		ok, partial   bool
		hits          int
		err           string
		skipped       []string
		fallbackEmpty int
	}{
		{
			name: "zero-hit fallback does not mask a failed primary",
			outcomes: []providerOutcome{
				{providerID: "brave", reason: "http_error", detail: "HTTP 422"},
				{providerID: "fb1", ok: true},
				{providerID: "fb2", reason: "paced"},
			},
			err: "brave: HTTP 422", skipped: []string{"brave"}, fallbackEmpty: 2,
		},
		{
			name: "fallback hits stand in for a failed primary",
			outcomes: []providerOutcome{
				{providerID: "brave", reason: "http_error"},
				{providerID: "fb1", ok: true, hits: []WebHit{hit}},
			},
			ok: true, partial: true, hits: 1, skipped: []string{"brave"},
		},
		{
			name: "unconfigured primary is an empty result",
			outcomes: []providerOutcome{
				{providerID: "brave", reason: "auth_missing"},
				{providerID: "fb1", reason: "http_error"},
			},
			ok: true, skipped: []string{"brave"}, fallbackEmpty: 1,
		},
		{
			name: "primary hits ignore fallbacks",
			outcomes: []providerOutcome{
				{providerID: "brave", ok: true, hits: []WebHit{hit}},
				{providerID: "fb1", reason: "http_error"},
			},
			ok: true, hits: 1, skipped: []string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := finalizeSearchResult("q", 5, tc.outcomes, soft)
			var skipped []string
			for _, s := range r.ProvidersSkipped {
				skipped = append(skipped, s.Provider)
			}
			if r.OK != tc.ok || r.Partial != tc.partial || len(r.Results) != tc.hits || r.Error != tc.err ||
				!slices.Equal(skipped, tc.skipped) && len(skipped)+len(tc.skipped) > 0 || r.FallbackEmptyCount != tc.fallbackEmpty {
				t.Fatalf("result = %+v", r)
			}
		})
	}
}
