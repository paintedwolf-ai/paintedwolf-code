package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestBundledProvidersDeclareHTTPRetry(t *testing.T) {
	t.Parallel()
	cfg, err := llm.LoadProviderConfig()
	contractcheck.FailErr(t, "llm.LoadProviderConfig", err)
	if len(cfg.Providers) < 15 {
		t.Fatalf("ship providers = %d, want >= 15", len(cfg.Providers))
	}
	for _, p := range cfg.Providers {
		if p.HTTPRetry.IsZero() {
			t.Errorf("provider %q: http_retry is required", p.ID)
			continue
		}
		if err := providerretry.ValidateHTTPRetry(p.HTTPRetry); err != nil {
			t.Errorf("provider %q: %v", p.ID, err)
		}
		if p.HTTPRetry.Capacity == nil {
			t.Errorf("provider %q: http_retry.capacity is required", p.ID)
			continue
		}
		for _, status := range p.HTTPRetry.Statuses {
			if status == 503 || status == 529 {
				t.Errorf("provider %q: HTTP %d belongs on http_retry.capacity, not the throttle list", p.ID, status)
			}
		}
	}
	if _, err := cfg.Capacity.Policy(); err != nil {
		t.Errorf("provider_capacity: %v", err)
	}
}
