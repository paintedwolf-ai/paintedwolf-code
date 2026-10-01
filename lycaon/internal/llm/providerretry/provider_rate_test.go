package providerretry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/filelock"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/testutil"
)

func rateFixture(t *testing.T, directory string) (ProviderAttempt, *time.Time) {
	t.Helper()
	now := time.Unix(1800000000, 0)
	gate := NewProviderRateGate(directory)
	gate.now = func() time.Time { return now }
	gate.jitter = func(window int) int { return window }
	gate.sleep = func(ctx context.Context, d time.Duration) error { now = now.Add(d); return ctx.Err() }
	policy := retryTestPolicy()
	policy.RateLimit = &ProviderRatePolicy{Adaptive: true, Scope: "provider", InitialMs: 1000, MaxBackoffMs: 8000, RecoverySuccesses: 2}
	policy.rateGate = gate
	policy.rateOrigin = "https://provider.example:443"
	return ProviderAttempt{ProviderID: "provider", Model: "model-a", Policy: policy}, &now
}

func rateState(t *testing.T, lease *Admission) providerRateState {
	t.Helper()
	var state providerRateState
	testutil.FailErr(t, "read rate state", lease.gate.update(t.Context(), lease.key, func(s *providerRateState) error { state = *s; return nil }))
	return state
}

func TestProviderRateCooldownAndPacingAreSharedAcrossModels(t *testing.T) {
	attempt, now := rateFixture(t, "")
	first, err := attempt.Admission(t.Context())
	testutil.FailErr(t, "admit first", err)
	old, err := attempt.Admission(t.Context())
	testutil.FailErr(t, "admit concurrent request", err)
	testutil.FailErr(t, "record throttling", first.Limited(t.Context(), nil, attempt.Policy))
	testutil.FailErr(t, "ignore stale success", old.Succeeded(t.Context()))
	if rateState(t, first).PenaltyMs != 1000 {
		t.Fatal("stale success cleared congestion")
	}
	before := *now
	attempt.Model = "model-b"
	second, err := attempt.Admission(t.Context())
	testutil.FailErr(t, "admit sibling model", err)
	if now.Sub(before) != time.Second {
		t.Fatal("sibling bypassed provider cooldown")
	}
	before = *now
	_, err = attempt.Admission(t.Context())
	testutil.FailErr(t, "pace next sibling", err)
	if now.Sub(before) != 500*time.Millisecond {
		t.Fatal("waiters burst together after cooldown")
	}
	testutil.FailErr(t, "record later congestion", second.Limited(t.Context(), nil, attempt.Policy))
	if rateState(t, first).PenaltyMs != 2000 {
		t.Fatal("congestion did not increase backoff")
	}
}

func TestProviderRateBucketScopeIsCatalogControlled(t *testing.T) {
	attempt, _ := rateFixture(t, "")
	attempt.Policy.RateLimit.Scope = "model"
	first, err := attempt.Admission(t.Context())
	testutil.FailErr(t, "admit first model", err)
	testutil.FailErr(t, "limit first model", first.Limited(t.Context(), nil, attempt.Policy))
	attempt.Model = "model-b"
	sibling, err := attempt.Admission(t.Context())
	testutil.FailErr(t, "admit independent model", err)
	if sibling.key == first.key || rateState(t, sibling).PenaltyMs != 0 {
		t.Fatal("model bucket throttled another model")
	}
	attempt.Policy.rateOrigin = "https://other.example:443"
	attempt.Model = "model-a"
	other, err := attempt.Admission(t.Context())
	testutil.FailErr(t, "admit independent provider", err)
	if other.key == first.key {
		t.Fatal("independent providers share a bucket")
	}
}

func TestProviderRateServerDeadlineOutranksCapAndStaleResponses(t *testing.T) {
	attempt, now := rateFixture(t, "")
	lease, err := attempt.Admission(t.Context())
	testutil.FailErr(t, "admit request", err)
	stale, err := attempt.Admission(t.Context())
	testutil.FailErr(t, "admit concurrent request", err)
	attempt.Policy.WaitHeaders = []string{"x-ratelimit-reset", "Retry-After"}
	headers := http.Header{"Retry-After": []string{"600"}, "X-Ratelimit-Reset": []string{"1.5"}}
	testutil.FailErr(t, "record long deadline", lease.Limited(t.Context(), headers, attempt.Policy))
	testutil.FailErr(t, "record concurrent rejection", stale.Limited(t.Context(), nil, attempt.Policy))
	state := rateState(t, lease)
	if state.PenaltyMs != 1000 || state.CooldownUntil != now.Add(600*time.Second).UnixMilli() {
		t.Fatalf("concurrent response changed server deadline or doubled one wave: %+v", state)
	}
	before := *now
	_, err = attempt.Admission(t.Context())
	testutil.FailErr(t, "wait for server deadline", err)
	if now.Sub(before) != 600*time.Second {
		t.Fatal("server wait was truncated")
	}
}

func TestProviderRateRecoversGraduallyAfterFreshSuccesses(t *testing.T) {
	attempt, _ := rateFixture(t, "")
	lease, err := attempt.Admission(t.Context())
	testutil.FailErr(t, "admit request", err)
	testutil.FailErr(t, "record first rejection", lease.Limited(t.Context(), nil, attempt.Policy))
	lease, err = attempt.Admission(t.Context())
	testutil.FailErr(t, "admit retry", err)
	testutil.FailErr(t, "record second rejection", lease.Limited(t.Context(), nil, attempt.Policy))
	for i := 0; i < 4; i++ {
		lease, err = attempt.Admission(t.Context())
		testutil.FailErr(t, "admit recovery", err)
		testutil.FailErr(t, "record recovery", lease.Succeeded(t.Context()))
		expected := []int{2000, 1000, 1000, 0}[i]
		if state := rateState(t, lease); state.PenaltyMs != expected {
			t.Fatalf("recovery %d: %+v", i, state)
		}
	}
}

func TestProviderRateObserveModeDoesNotChangeRetryBehavior(t *testing.T) {
	attempt, now := rateFixture(t, "")
	attempt.Policy.RateLimit.Adaptive = false
	before := *now
	lease, err := attempt.Admission(t.Context())
	testutil.FailErr(t, "admit observed request", err)
	testutil.FailErr(t, "observe rejection", lease.Limited(t.Context(), nil, attempt.Policy))
	_, err = attempt.Admission(t.Context())
	testutil.FailErr(t, "admit unchanged schedule", err)
	state := rateState(t, lease)
	if !now.Equal(before) || state.Limited != 1 || state.PenaltyMs != 0 || state.Admitted != 2 {
		t.Fatalf("observation changed scheduling: %+v", state)
	}
	calls := 0
	attempt.Send = func(context.Context) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	}
	response, err := RunProviderAttempts(t.Context(), attempt)
	if response != nil {
		_ = response.Body.Close()
	}
	if !errors.Is(err, failure.ErrProviderRateLimited) || calls != attempt.Policy.MaxRetries+1 {
		t.Fatalf("observe mode changed exhaustion: calls=%d err=%v", calls, err)
	}
}

func TestProviderRateAdaptiveRetriesStayInsideOneProviderRequest(t *testing.T) {
	attempt, _ := rateFixture(t, "")
	calls := 0
	attempt.Send = func(context.Context) (*http.Response, error) {
		calls++
		code := 429
		if calls == 8 {
			code = 200
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	}
	response, err := RunProviderAttempts(t.Context(), attempt)
	testutil.FailErr(t, "recover original provider request", err)
	_ = response.Body.Close()
	if calls != 8 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestProviderRateCancellationStopsQueuedAdmission(t *testing.T) {
	attempt, _ := rateFixture(t, "")
	lease, err := attempt.Admission(t.Context())
	testutil.FailErr(t, "admit first", err)
	testutil.FailErr(t, "record throttling", lease.Limited(t.Context(), nil, attempt.Policy))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = attempt.Admission(ctx)
	if !errors.Is(err, context.Canceled) || rateState(t, lease).Admitted != 1 {
		t.Fatal("cancellation admitted another request")
	}
}

func TestProviderRateSharedStateSurvivesProcessExit(t *testing.T) {
	directory := t.TempDir()
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestProviderRateProcessHelper$")
	command.Env = append(os.Environ(), "PW_RATE_TEST_STATE="+directory)
	body, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("write child rate state: %v: %s", err, body)
	}
	attempt, now := rateFixture(t, directory)
	before := *now
	lease, err := attempt.Admission(t.Context())
	testutil.FailErr(t, "rejoin shared bucket", err)
	if now.Sub(before) != time.Second || rateState(t, lease).Limited != 1 {
		t.Fatal("process exit lost cooldown")
	}
}

func TestProviderRateProcessHelper(t *testing.T) {
	directory := os.Getenv("PW_RATE_TEST_STATE")
	if directory == "" {
		return
	}
	attempt, _ := rateFixture(t, directory)
	lease, err := attempt.Admission(t.Context())
	testutil.FailErr(t, "child admission", err)
	testutil.FailErr(t, "child throttle", lease.Limited(t.Context(), nil, attempt.Policy))
}

func TestProviderRateCorruptStateDoesNotFailOpen(t *testing.T) {
	directory := t.TempDir()
	attempt, _ := rateFixture(t, directory)
	lease, err := attempt.Admission(t.Context())
	testutil.FailErr(t, "admit initial request", err)
	path := filepath.Join(directory, lease.key+".json")
	state := rateState(t, lease)
	state.Version = 99
	body, err := json.Marshal(state)
	testutil.FailErr(t, "encode unsupported state", err)
	testutil.FailErr(t, "replace test state", os.WriteFile(path, body, 0o600))
	if _, err := attempt.Admission(t.Context()); err == nil {
		t.Fatal("unsupported state bypassed provider gate")
	}
	for _, invalid := range []string{"null", "{}", "{"} {
		testutil.FailErr(t, "write corrupt state", os.WriteFile(path, []byte(invalid), 0o600))
		if _, err := attempt.Admission(t.Context()); err == nil {
			t.Fatalf("corrupt state %q bypassed provider gate", invalid)
		}
	}
}

func TestProviderRateDoesNotConsumeOtherFaultRetryBudget(t *testing.T) {
	attempt, _ := rateFixture(t, "")
	calls := 0
	attempt.Send = func(context.Context) (*http.Response, error) {
		calls++
		code := 429
		switch calls {
		case 8:
			code = 503
		case 9:
			code = 200
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	}
	response, err := RunProviderAttempts(t.Context(), attempt)
	testutil.FailErr(t, "recover after throttling and a transient failure", err)
	_ = response.Body.Close()
	if calls != 9 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestProviderRateFileLockWaitIsCancelable(t *testing.T) {
	attempt, _ := rateFixture(t, t.TempDir())
	lease, err := attempt.Admission(t.Context())
	testutil.FailErr(t, "initial admission", err)
	lock, err := filelock.Open(filepath.Join(lease.gate.directory, lease.key+".lock"))
	testutil.FailErr(t, "open lock", err)
	defer func() { _ = lock.Close() }()
	held, err := filelock.TryExclusive(lock)
	testutil.FailErr(t, "hold lock", err)
	if !held {
		t.Fatal("could not hold fixture lock")
	}
	defer func() { _ = filelock.Unlock(lock) }()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err = attempt.Admission(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting admission did not cancel: %v", err)
	}
}

func TestProviderRateObservationWindowsAreBoundedAndCountAcceptedRequests(t *testing.T) {
	attempt, now := rateFixture(t, "")
	attempt.Policy.RateLimit.Adaptive = false
	var lease *Admission
	for i := 0; i < 120; i++ {
		var err error
		lease, err = attempt.Admission(t.Context())
		testutil.FailErr(t, "admit observed request", err)
		testutil.FailErr(t, "accept observed request", lease.Succeeded(t.Context()))
		*now = now.Add(time.Minute)
	}
	state := rateState(t, lease)
	if len(state.Windows) != 60 || state.Admitted != 120 || state.Accepted != 120 {
		t.Fatalf("observation retention or lifetime totals: %+v", state)
	}
	for _, window := range state.Windows {
		if window.Admitted != 1 || window.Accepted != 1 || window.Limited != 0 {
			t.Fatalf("unexpected observation window: %+v", window)
		}
	}
}

func TestProviderRatePolicyValidationAndClone(t *testing.T) {
	attempt, _ := rateFixture(t, "")
	testutil.FailErr(t, "validate adaptive policy", ValidateHTTPRetry(attempt.Policy))
	for _, mutate := range []func(*ProviderHTTPRetry){
		func(p *ProviderHTTPRetry) { p.RateLimit.Scope = "unknown" },
		func(p *ProviderHTTPRetry) { p.RateLimit.InitialMs = 0 },
		func(p *ProviderHTTPRetry) { p.RateLimit.MaxBackoffMs = 1 },
		func(p *ProviderHTTPRetry) { p.RateLimit.RecoverySuccesses = 0 },
		func(p *ProviderHTTPRetry) { p.Statuses = []int{500} },
		func(p *ProviderHTTPRetry) { p.Capacity.RateLimit = p.RateLimit },
	} {
		policy := attempt.Policy.Clone()
		mutate(&policy)
		if err := ValidateHTTPRetry(policy); err == nil {
			t.Fatal("invalid rate policy accepted")
		}
		testutil.FailErr(t, "clone must not mutate source policy", ValidateHTTPRetry(attempt.Policy))
	}
}
