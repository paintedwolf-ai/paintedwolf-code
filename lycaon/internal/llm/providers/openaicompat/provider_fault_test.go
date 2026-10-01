package openaicompat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/providerhttp"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/testutil"
)

// silentThenServing sends no response headers for the first callsBeforeAnswer
// requests, then serves body. Parked handlers outlive the client's header
// timeout, so cleanup releases them before closing the server.
func silentThenServing(t *testing.T, callsBeforeAnswer int32, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= callsBeforeAnswer {
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	return srv, &calls
}

// transportTestPolicy retries transport faults quickly enough for a unit test.
func transportTestPolicy(maxRetries int) providerretry.ProviderHTTPRetry {
	return providerretry.ProviderHTTPRetry{
		MaxRetries:  1,
		MaxWaitMs:   1000,
		BackoffMs:   []int{10},
		Statuses:    []int{429},
		WaitHeaders: []string{"Retry-After"},
		Transport: &providerretry.ProviderTransportRetry{
			MaxRetries: maxRetries,
			MaxWaitMs:  1000,
			BackoffMs:  []int{10, 10},
		},
	}
}

func shortHeaderClient() *http.Client {
	return providerhttp.NewStreamingClient(300 * time.Millisecond)
}

// A provider that accepts the request and sends no response headers is
// reissued, and the second attempt is served.
func TestSilentProviderIsReissuedAndRecovers(t *testing.T) {
	const completion = `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
	srv, calls := silentThenServing(t, 1, completion)

	p := New("together-test", srv.URL, "k", nil)
	p.WithHTTPRetry(transportTestPolicy(1))

	resp, err := p.postChatWithHTTPRetry(context.Background(), "m", []byte(`{}`), shortHeaderClient(), nil)
	if err != nil {
		testutil.FailErr(t, "post through a provider that went quiet once", err)
	}
	if closeErr := resp.Body.Close(); closeErr != nil {
		testutil.FailErr(t, "close recovered response", closeErr)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("want 2 attempts (one silent, one served), got %d", got)
	}
}

// A provider that never answers spends its schedule and reports what happened,
// including that the request did arrive.
func TestSilentProviderExhaustsIntoATypedFault(t *testing.T) {
	srv, calls := silentThenServing(t, 99, "")

	p := New("together-test", srv.URL, "k", nil)
	p.WithHTTPRetry(transportTestPolicy(1))

	//nolint:bodyclose // No response is returned on this path.
	_, err := p.postChatWithHTTPRetry(context.Background(), "m", []byte(`{}`), shortHeaderClient(), nil)
	if err == nil {
		t.Fatal("want a spent transport budget, got success")
	}
	silent, ok := failure.AsProviderSilent(err)
	if !ok {
		t.Fatalf("want a ProviderSilentError, got %T: %v", err, err)
	}
	if silent.ProviderID != "together-test" {
		t.Fatalf("want the provider named, got %q", silent.ProviderID)
	}
	if silent.Model != "m" {
		t.Fatalf("want the model named, got %q", silent.Model)
	}
	if silent.Attempts != 2 {
		t.Fatalf("want 2 attempts recorded, got %d", silent.Attempts)
	}
	if silent.Silence <= 0 {
		t.Fatalf("want the dead air recorded, got %v", silent.Silence)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("want 2 attempts, got %d", got)
	}
	// A silent provider must never be reported as one that was never reached:
	// the request landed, and the copy for that case promises nothing was spent.
	if _, wrong := failure.AsProviderUnreachable(err); wrong {
		t.Fatal("a delivered request must not classify as unreachable")
	}
}

// A provider that cannot be reached is a different fault with a different
// promise attached to it.
func TestUnreachableProviderIsTypedSeparately(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // Nothing is listening now.

	p := New("dead-test", url, "k", nil)
	p.WithHTTPRetry(transportTestPolicy(1))

	//nolint:bodyclose // No response is returned on this path.
	_, err := p.postChatWithHTTPRetry(context.Background(), "m", []byte(`{}`), shortHeaderClient(), nil)
	if err == nil {
		t.Fatal("want a dial failure, got success")
	}
	unreachable, ok := failure.AsProviderUnreachable(err)
	if !ok {
		t.Fatalf("want a ProviderUnreachableError, got %T: %v", err, err)
	}
	if unreachable.Attempts != 2 {
		t.Fatalf("want 2 attempts recorded, got %d", unreachable.Attempts)
	}
	if unreachable.ProviderID != "dead-test" || unreachable.Model != "m" {
		t.Fatalf("unreachable provenance = %+v", unreachable)
	}
	if _, wrong := failure.AsProviderSilent(err); wrong {
		t.Fatal("an undelivered request must not classify as silent")
	}
}

// A canceled turn is the caller leaving, not a provider fault: no retry, and
// the context error is what comes back.
func TestCanceledContextIsNotRetried(t *testing.T) {
	srv, calls := silentThenServing(t, 99, "")

	p := New("together-test", srv.URL, "k", nil)
	p.WithHTTPRetry(transportTestPolicy(3))

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	//nolint:bodyclose // No response is returned on this path.
	_, err := p.postChatWithHTTPRetry(ctx, "m", []byte(`{}`), shortHeaderClient(), nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the context error, got %T: %v", err, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("want exactly 1 attempt before the caller left, got %d", got)
	}
}

// Retries reach the observer with a reason that names the fault and carries the
// silence, so the composer can say why nothing is happening.
func TestSilentRetryIsReportedToTheObserver(t *testing.T) {
	srv, _ := silentThenServing(t, 99, "")

	p := New("together-test", srv.URL, "k", nil)
	p.WithHTTPRetry(transportTestPolicy(1))

	var seen []providerretry.RetryAttempt
	ctx := providerretry.WithRetryObserver(context.Background(), func(a providerretry.RetryAttempt) {
		seen = append(seen, a)
	})

	//nolint:bodyclose // No response is returned on this path.
	_, _ = p.postChatWithHTTPRetry(ctx, "m", []byte(`{}`), shortHeaderClient(), nil)

	if len(seen) != 1 {
		t.Fatalf("want one retry reported, got %d: %+v", len(seen), seen)
	}
	if seen[0].Reason != providerretry.RetryReasonSilent {
		t.Fatalf("want reason %q, got %q", providerretry.RetryReasonSilent, seen[0].Reason)
	}
	if seen[0].Silence <= 0 {
		t.Fatalf("want the silence carried on the retry, got %v", seen[0].Silence)
	}
	if seen[0].Status != 0 {
		t.Fatalf("a transport fault has no status, got %d", seen[0].Status)
	}
}

// Opting out is explicit and honoured.
func TestTransportRetriesCanBeTurnedOff(t *testing.T) {
	srv, calls := silentThenServing(t, 99, "")

	p := New("together-test", srv.URL, "k", nil)
	policy := transportTestPolicy(0)
	p.WithHTTPRetry(policy)

	//nolint:bodyclose // No response is returned on this path.
	_, err := p.postChatWithHTTPRetry(context.Background(), "m", []byte(`{}`), shortHeaderClient(), nil)
	if _, ok := failure.AsProviderSilent(err); !ok {
		t.Fatalf("want a silent fault, got %T: %v", err, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("want a single attempt when transport retries are off, got %d", got)
	}
}
