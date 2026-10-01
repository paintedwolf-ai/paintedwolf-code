package providerretry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func availabilityTestPolicy() ProviderHTTPRetry {
	return ProviderHTTPRetry{MaxRetries: 2, MaxWaitMs: 10, BackoffMs: []int{1, 1},
		Statuses: []int{429, 500, 502, 503, 504}, WaitHeaders: []string{"Retry-After"},
		Availability: &ProviderAvailabilityRetry{InitialMs: 1, MaxBackoffMs: 2}}
}

func TestAvailabilityLeavesNormalRetryLimitsUnchanged(t *testing.T) {
	policy := availabilityTestPolicy()
	fault := Fault{Kind: FaultRateLimited, Status: 429}
	if !ShouldRetryFault(policy, 10000, fault) {
		t.Error("availability retry exhausted")
	}
	policy.Availability = nil
	if ShouldRetryFault(policy, 2, fault) {
		t.Error("normal retry allowance widened")
	}
	if !ShouldRetryFault(policy, 1, fault) {
		t.Error("normal retry removed")
	}
}

func TestAvailabilityNeverWidensModelOutputRetries(t *testing.T) {
	policy := availabilityTestPolicy()
	for _, fault := range []Fault{{Kind: FaultEmptyCompletion}, {Kind: FaultModelRefused, Status: 503},
		{Kind: FaultCanceled}, {Kind: FaultStatus, Status: 401}, {Kind: FaultStatus, Status: 400}, {Kind: FaultStatus, Status: 501}} {
		if ShouldRetryFault(policy, 10000, fault) {
			t.Errorf("retried non-availability fault: %+v", fault)
		}
	}
	for _, kind := range []FaultKind{FaultUnreachable, FaultSilent} {
		if !ShouldRetryFault(policy, 10000, Fault{Kind: kind}) {
			t.Errorf("did not wait for %s", kind)
		}
	}
}

func TestAvailabilityRecoversSameRequestAfterExtendedOutage(t *testing.T) {
	calls := 0
	response, err := RunProviderAttempts(context.Background(), ProviderAttempt{ProviderID: "fixture", Model: "candidate", Policy: availabilityTestPolicy(),
		Send: func(ctx context.Context) (*http.Response, error) {
			calls++
			status := 503
			if calls == 13 {
				status = 200
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("response"))}, nil
		}})
	testutil.FailErr(t, "recover provider request", err)
	defer response.Body.Close()
	if calls != 13 || response.StatusCode != 200 {
		t.Errorf("calls=%d status=%d", calls, response.StatusCode)
	}
}

func TestAvailabilityHonorsCancellationAndServerDeadline(t *testing.T) {
	policy := availabilityTestPolicy()
	fault := Fault{Kind: FaultRateLimited, Status: 429, Header: http.Header{"Retry-After": []string{"7200"}}}
	if got := policy.availabilityWait(10000, fault); got < 2*time.Hour {
		t.Errorf("server deadline shortened: %v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := AwaitFaultRetry(ctx, policy, 10000, fault); !errors.Is(err, context.Canceled) {
		t.Errorf("cancellation: %v", err)
	}
	if maxAttemptsForFault(policy, fault) != 0 {
		t.Error("unlimited retry has a finite display limit")
	}
}

func TestAvailabilityValidationAndClone(t *testing.T) {
	policy := availabilityTestPolicy()
	testutil.FailErr(t, "validate availability", ValidateHTTPRetry(policy))
	cloned := policy.Clone()
	cloned.Availability.InitialMs = 0
	if policy.Availability.InitialMs != 1 {
		t.Error("clone mutated original")
	}
	if ValidateHTTPRetry(cloned) == nil {
		t.Error("zero backoff accepted")
	}
	cloned.Availability = &ProviderAvailabilityRetry{InitialMs: 2, MaxBackoffMs: 1}
	if ValidateHTTPRetry(cloned) == nil {
		t.Error("inverted backoff accepted")
	}
}

func TestAvailabilityCooldownSurvivesAnotherProcessGate(t *testing.T) {
	directory := t.TempDir()
	first, now := rateFixture(t, directory)
	first.Policy.RateLimit.Adaptive = false
	first.Policy.Availability = availabilityTestPolicy().Availability
	admission, err := first.Admission(t.Context())
	testutil.FailErr(t, "admit unavailable provider", err)
	testutil.FailErr(t, "persist availability cooldown", admission.unavailable(t.Context(), 2*time.Hour))
	second, recoveredNow := rateFixture(t, directory)
	second.Policy.RateLimit.Adaptive = false
	second.Policy.Availability = availabilityTestPolicy().Availability
	second.Model = "other-model"
	_, err = second.Admission(t.Context())
	testutil.FailErr(t, "respect retained provider cooldown", err)
	if recoveredNow.Sub(*now) != 2*time.Hour {
		t.Errorf("wait=%v", recoveredNow.Sub(*now))
	}
}
