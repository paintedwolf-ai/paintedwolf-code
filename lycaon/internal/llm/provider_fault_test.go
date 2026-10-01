package llm

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A provider entry that never mentions transport still gets the schedule. A
// local provider file replaces http_retry wholesale, so anything it does not
// restate would otherwise be gone.
func TestPolicyWithoutATransportBlockStillRetries(t *testing.T) {
	policy := providerretry.ProviderHTTPRetry{
		MaxRetries:  3,
		MaxWaitMs:   60000,
		BackoffMs:   []int{1000, 2000, 4000},
		Statuses:    []int{429, 503},
		WaitHeaders: []string{"Retry-After"},
	}
	fault := providerretry.Fault{Kind: providerretry.FaultSilent, Elapsed: 30 * time.Second}
	if !providerretry.ShouldRetryFault(policy, 0, fault) {
		t.Fatal("a policy with no transport block must still reissue a silent request")
	}
	if providerretry.ShouldRetryFault(policy, 1, fault) {
		t.Fatal("the default schedule allows one retry, not two")
	}
	if !providerretry.ShouldRetryFault(policy, 0, providerretry.Fault{Kind: providerretry.FaultEmptyCompletion}) {
		t.Fatal("the default schedule must retry an empty completion before output")
	}
}

func TestStatusFaultsKeepTheirExistingSchedule(t *testing.T) {
	policy := providerretry.ProviderHTTPRetry{
		MaxRetries:  2,
		MaxWaitMs:   60000,
		BackoffMs:   []int{1000, 2000},
		Statuses:    []int{429},
		WaitHeaders: []string{"Retry-After"},
		Capacity: &providerretry.ProviderHTTPRetry{
			MaxRetries: 2, MaxWaitMs: 60000, BackoffMs: []int{2000, 8000},
			Statuses: []int{503}, WaitHeaders: []string{"Retry-After"},
		},
	}
	for _, tc := range []struct {
		name    string
		status  int
		kind    providerretry.FaultKind
		attempt int
		want    bool
	}{
		{"rate limit first retry", 429, providerretry.FaultRateLimited, 0, true},
		{"rate limit budget spent", 429, providerretry.FaultRateLimited, 2, false},
		{"capacity first retry", 503, providerretry.FaultCapacity, 0, true},
		{"unlisted status never retries", 400, providerretry.FaultStatus, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := providerretry.ShouldRetryFault(policy, tc.attempt, providerretry.Fault{Kind: tc.kind, Status: tc.status})
			if got != tc.want {
				t.Fatalf("ShouldRetryFault(%d, %d) = %v, want %v", tc.attempt, tc.status, got, tc.want)
			}
		})
	}
}

// Every shipped provider must end up with a working response-fault schedule,
// whether it declares one or inherits the default.
func TestEveryShippedProviderRetriesSafeResponseFaults(t *testing.T) {
	cfg, err := LoadProviderConfig()
	if err != nil {
		testutil.FailErr(t, "load shipped providers.yaml", err)
	}
	if len(cfg.Providers) == 0 {
		t.Fatal("shipped providers.yaml declared no providers")
	}
	for _, p := range cfg.Providers {
		if err := providerretry.ValidateHTTPRetry(p.HTTPRetry); err != nil {
			testutil.FailErr(t, "validate http_retry for "+p.ID, err)
		}
		transport := p.HTTPRetry.TransportPolicy()
		if transport.MaxRetries < 1 {
			t.Errorf("provider %s has no transport retry budget", p.ID)
		}
		if !transport.Covers(providerretry.FaultUnreachable) {
			t.Errorf("provider %s would not reissue a request that never reached it", p.ID)
		}
		if !transport.Covers(providerretry.FaultEmptyCompletion) {
			t.Errorf("provider %s would not reissue a safely replayable empty completion", p.ID)
		}
	}
}

// The capacity plane rests a silent slot longer than an overloaded one, because
// silence costs the whole header bound before anything is learned.
func TestShippedCapacityRestsSilenceLongest(t *testing.T) {
	cfg, err := LoadProviderConfig()
	if err != nil {
		testutil.FailErr(t, "load shipped providers.yaml", err)
	}
	policy, err := cfg.Capacity.Policy()
	if err != nil {
		testutil.FailErr(t, "resolve provider_capacity", err)
	}
	if policy.SilentMs <= policy.CooldownMs {
		t.Fatalf("silent cooldown %dms must exceed the overload cooldown %dms",
			policy.SilentMs, policy.CooldownMs)
	}
}
