package providerretry

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func retryTestPolicy() ProviderHTTPRetry {
	return ProviderHTTPRetry{
		MaxRetries:  2,
		MaxWaitMs:   50,
		BackoffMs:   []int{1, 1},
		Statuses:    []int{429},
		WaitHeaders: []string{"retry-after-ms"},
		Capacity: &ProviderHTTPRetry{
			MaxRetries:  2,
			MaxWaitMs:   50,
			BackoffMs:   []int{1, 1},
			Statuses:    []int{503},
			WaitHeaders: []string{"retry-after-ms"},
		},
		Transport: &ProviderTransportRetry{
			MaxRetries: 1,
			MaxWaitMs:  50,
			BackoffMs:  []int{1},
		},
	}
}

// statusFault builds the fault an HTTP answer produces, so these tests drive
// the same entry point the adapters do.
func statusFault(status int, hdr http.Header) Fault {
	if hdr == nil {
		hdr = http.Header{}
	}
	return Fault{Kind: faultKindForStatus(status), Status: status, Header: hdr}
}

func TestAwaitFaultRetryReportsTheAttemptAboutToRun(t *testing.T) {
	var seen []RetryAttempt
	ctx := WithRetryObserver(context.Background(), func(a RetryAttempt) {
		seen = append(seen, a)
	})

	if err := AwaitFaultRetry(ctx, retryTestPolicy(), 0, statusFault(429, nil)); err != nil {
		t.Fatalf("AwaitFaultRetry(attempt 0) = %v want nil", err)
	}
	if err := AwaitFaultRetry(ctx, retryTestPolicy(), 1, statusFault(503, nil)); err != nil {
		t.Fatalf("AwaitFaultRetry(attempt 1) = %v want nil", err)
	}

	if len(seen) != 2 {
		t.Fatalf("observed %d retries, want 2", len(seen))
	}
	// attempt counts completed failures; the next run is attempt+2.
	if seen[0].Attempt != 2 || seen[0].MaxAttempts != 3 {
		t.Fatalf("first retry = %d/%d want 2/3", seen[0].Attempt, seen[0].MaxAttempts)
	}
	if seen[1].Attempt != 3 || seen[1].MaxAttempts != 3 {
		t.Fatalf("second retry = %d/%d want 3/3", seen[1].Attempt, seen[1].MaxAttempts)
	}
	if seen[0].Status != 429 || seen[1].Status != 503 {
		t.Fatalf("statuses = %d,%d want 429,503", seen[0].Status, seen[1].Status)
	}
	if seen[0].Reason != RetryReasonStatus {
		t.Fatalf("429 reason = %q want %q", seen[0].Reason, RetryReasonStatus)
	}
	if seen[1].Reason != RetryReasonCapacity {
		t.Fatalf("503 reason = %q want %q", seen[1].Reason, RetryReasonCapacity)
	}
	for i, a := range seen {
		if a.Wait <= 0 {
			t.Fatalf("retry %d wait = %v want a positive backoff", i, a.Wait)
		}
		if a.Silence != 0 {
			t.Fatalf("retry %d carried silence %v; only a silent provider has any", i, a.Silence)
		}
	}
}

func TestAwaitFaultRetryReportsTheHonoredWaitHeader(t *testing.T) {
	var seen RetryAttempt
	ctx := WithRetryObserver(context.Background(), func(a RetryAttempt) { seen = a })
	hdr := http.Header{}
	hdr.Set("retry-after-ms", "40")

	if err := AwaitFaultRetry(ctx, retryTestPolicy(), 0, statusFault(429, hdr)); err != nil {
		t.Fatalf("AwaitFaultRetry = %v want nil", err)
	}
	if seen.Wait != 40*time.Millisecond {
		t.Fatalf("wait = %v want 40ms from the retry-after-ms header", seen.Wait)
	}
}

// A transport fault has no status and no response header to read a wait from,
// so it reports its own schedule and the dead air that produced it.
func TestAwaitFaultRetryReportsSilenceWithNoStatus(t *testing.T) {
	var seen RetryAttempt
	ctx := WithRetryObserver(context.Background(), func(a RetryAttempt) { seen = a })

	fault := Fault{Kind: FaultSilent, Elapsed: 30 * time.Second}
	if err := AwaitFaultRetry(ctx, retryTestPolicy(), 0, fault); err != nil {
		t.Fatalf("AwaitFaultRetry = %v want nil", err)
	}
	if seen.Reason != RetryReasonSilent {
		t.Fatalf("reason = %q want %q", seen.Reason, RetryReasonSilent)
	}
	if seen.Status != 0 {
		t.Fatalf("status = %d want 0 for a fault that never got one", seen.Status)
	}
	if seen.Silence != 30*time.Second {
		t.Fatalf("silence = %v want 30s", seen.Silence)
	}
	if seen.MaxAttempts != 2 {
		t.Fatalf("max attempts = %d want 2 from the transport schedule", seen.MaxAttempts)
	}
}

func TestAwaitFaultRetryWithoutObserverStillSleeps(t *testing.T) {
	if err := AwaitFaultRetry(context.Background(), retryTestPolicy(), 0, statusFault(429, nil)); err != nil {
		t.Fatalf("AwaitFaultRetry with no observer = %v want nil", err)
	}
}

func TestAwaitFaultRetryReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := AwaitFaultRetry(ctx, retryTestPolicy(), 0, statusFault(429, nil)); err == nil {
		t.Fatal("AwaitFaultRetry on a canceled context = nil want ctx.Err()")
	}
}
