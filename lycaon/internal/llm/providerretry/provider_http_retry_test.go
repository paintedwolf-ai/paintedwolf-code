package providerretry

import (
	"net/http"
	"testing"
	"time"
)

func TestValidateHTTPRetry(t *testing.T) {
	good := ProviderHTTPRetry{
		MaxRetries:  2,
		MaxWaitMs:   60000,
		BackoffMs:   []int{1000, 2000},
		Statuses:    []int{429, 503},
		WaitHeaders: []string{"Retry-After"},
	}
	if err := ValidateHTTPRetry(good); err != nil {
		t.Fatalf("good policy: %v", err)
	}
	disabled := ProviderHTTPRetry{
		MaxRetries:  0,
		MaxWaitMs:   30000,
		Statuses:    []int{429, 503},
		WaitHeaders: []string{"Retry-After"},
	}
	if err := ValidateHTTPRetry(disabled); err != nil {
		t.Fatalf("disabled policy: %v", err)
	}
	badBackoff := good
	badBackoff.BackoffMs = []int{1000}
	if err := ValidateHTTPRetry(badBackoff); err == nil {
		t.Fatal("expected backoff length error")
	}
	emptyStatuses := good
	emptyStatuses.Statuses = nil
	if err := ValidateHTTPRetry(emptyStatuses); err == nil {
		t.Fatal("expected empty statuses error")
	}
}

func TestWaitDurationHeaders(t *testing.T) {
	policy := ProviderHTTPRetry{
		MaxRetries:  2,
		MaxWaitMs:   5000,
		BackoffMs:   []int{1000, 2000},
		Statuses:    []int{429},
		WaitHeaders: []string{"Retry-After"},
	}
	hdr := http.Header{}
	hdr.Set("Retry-After", "2")
	if got := waitDuration(policy, 0, hdr); got != 2*time.Second {
		t.Fatalf("Retry-After = %v want 2s", got)
	}

	msPolicy := policy
	msPolicy.WaitHeaders = []string{"retry-after-ms"}
	hdr = http.Header{}
	hdr.Set("retry-after-ms", "1500")
	if got := waitDuration(msPolicy, 0, hdr); got != 1500*time.Millisecond {
		t.Fatalf("retry-after-ms = %v want 1.5s", got)
	}

	together := policy
	together.WaitHeaders = []string{"x-ratelimit-reset", "Retry-After"}
	hdr = http.Header{}
	hdr.Set("x-ratelimit-reset", "3")
	if got := waitDuration(together, 0, hdr); got != 3*time.Second {
		t.Fatalf("x-ratelimit-reset = %v want 3s", got)
	}

	allDeadlines := together
	hdr = http.Header{}
	hdr.Set("x-ratelimit-reset", "1")
	hdr.Set("Retry-After", "9")
	if got := waitDuration(allDeadlines, 0, hdr); got != 9*time.Second {
		t.Fatalf("longest server deadline = %v want 9s", got)
	}

	capPolicy := policy
	capPolicy.MaxWaitMs = 500
	hdr = http.Header{}
	hdr.Set("Retry-After", "10")
	if got := waitDuration(capPolicy, 0, hdr); got != 10*time.Second {
		t.Fatalf("server deadline = %v want 10s", got)
	}

	noHdr := policy
	got := waitDuration(noHdr, 0, http.Header{})
	if got < time.Second || got > time.Second+250*time.Millisecond {
		t.Fatalf("backoff+jitter = %v want ~1s..1.25s", got)
	}
}

func TestRateHeadersRejectInvalidDurationsAndAcceptFractionalReset(t *testing.T) {
	for _, value := range []string{"NaN", "+Inf", "-1", "1e100", "invalid"} {
		if _, ok := parseWaitHeader("x-ratelimit-reset", http.Header{"X-Ratelimit-Reset": []string{value}}); ok {
			t.Fatalf("accepted invalid reset %q", value)
		}
	}
	if got, ok := parseWaitHeader("x-ratelimit-reset", http.Header{"X-Ratelimit-Reset": []string{"1.25"}}); !ok || got != 1250*time.Millisecond {
		t.Fatalf("fractional reset=%v valid=%v", got, ok)
	}
}

func TestShouldRetry(t *testing.T) {
	policy := ProviderHTTPRetry{
		MaxRetries:  2,
		MaxWaitMs:   1000,
		BackoffMs:   []int{1, 1},
		Statuses:    []int{429, 503},
		WaitHeaders: []string{"Retry-After"},
	}
	if !ShouldRetry(policy, 0, 429) {
		t.Fatal("attempt 0 + 429 should retry")
	}
	if ShouldRetry(policy, 2, 429) {
		t.Fatal("attempt 2 should not retry when max_retries=2")
	}
	if ShouldRetry(policy, 0, 400) {
		t.Fatal("400 should not retry")
	}
	disabled := policy
	disabled.MaxRetries = 0
	if ShouldRetry(disabled, 0, 429) {
		t.Fatal("max_retries 0 should not retry")
	}
}

func TestHTTPRetryCapacitySchedule(t *testing.T) {
	policy := ProviderHTTPRetry{
		MaxRetries:  3,
		MaxWaitMs:   1000,
		BackoffMs:   []int{1, 1, 1},
		Statuses:    []int{429},
		WaitHeaders: []string{"Retry-After"},
		Capacity: &ProviderHTTPRetry{
			MaxRetries:  2,
			MaxWaitMs:   1000,
			BackoffMs:   []int{1, 1},
			Statuses:    []int{503},
			WaitHeaders: []string{"Retry-After"},
		},
	}
	if err := ValidateHTTPRetry(policy); err != nil {
		t.Fatalf("valid split policy: %v", err)
	}
	if !ShouldRetry(policy, 2, 429) {
		t.Fatal("throttle attempt 2 of 3 should retry")
	}
	if ShouldRetry(policy, 2, 503) {
		t.Fatal("capacity max_retries 2 must stop at attempt 2")
	}
	if !ShouldRetry(policy, 1, 503) {
		t.Fatal("capacity attempt 1 of 2 should retry")
	}
	if retryReasonForFault(statusFault(429, nil)) != RetryReasonStatus {
		t.Fatal("429 reason should be status")
	}
	if retryReasonForFault(statusFault(503, nil)) != RetryReasonCapacity {
		t.Fatal("503 reason should be capacity")
	}
	if retryReasonForFault(statusFault(529, nil)) != RetryReasonCapacity {
		t.Fatal("529 reason should be capacity")
	}
	if retryReasonForFault(Fault{Kind: FaultSilent}) != RetryReasonSilent {
		t.Fatal("a silent provider should be named as such, not as a status retry")
	}
	if retryReasonForFault(Fault{Kind: FaultUnreachable}) != RetryReasonUnreachable {
		t.Fatal("an undelivered request should be named as such")
	}

	overlap := policy
	overlap.Statuses = []int{429, 503}
	if err := ValidateHTTPRetry(overlap); err == nil {
		t.Fatal("expected overlap error")
	}
	nested := policy
	nested.Capacity.Capacity = &ProviderHTTPRetry{Statuses: []int{500}}
	if err := ValidateHTTPRetry(nested); err == nil {
		t.Fatal("expected nested capacity error")
	}
}

func TestProviderHTTPRetryIsZeroClone(t *testing.T) {
	var z ProviderHTTPRetry
	if !z.IsZero() {
		t.Fatal("empty should be zero")
	}
	p := ProviderHTTPRetry{
		MaxRetries:  0,
		MaxWaitMs:   30000,
		Statuses:    []int{429},
		WaitHeaders: []string{"Retry-After"},
	}
	if p.IsZero() {
		t.Fatal("bedrock-shaped disabled policy must not be IsZero")
	}
	c := p.Clone()
	c.Statuses[0] = 500
	if p.Statuses[0] != 429 {
		t.Fatal("Clone must deep-copy Statuses")
	}
}
