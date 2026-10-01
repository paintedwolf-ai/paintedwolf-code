package providerretry

import (
	"context"
	"net/http"
	"time"

	"github.com/lycaon/lycaon/internal/httpclient"
)

// FaultKind classifies one provider attempt.
type FaultKind string

const (
	// FaultNone is a response the caller can read.
	FaultNone FaultKind = ""
	// FaultStatus is a general non-2xx response.
	FaultStatus FaultKind = "status"
	// FaultRateLimited is HTTP 429.
	FaultRateLimited FaultKind = "rate_limited"
	// FaultCapacity is HTTP 503 or 529.
	FaultCapacity FaultKind = "capacity"
	// FaultUnreachable stopped before delivery.
	FaultUnreachable FaultKind = "unreachable"
	// FaultSilent timed out after delivery.
	FaultSilent FaultKind = "silent"
	// FaultEmptyCompletion is a successful terminal response with no usable
	// assistant content or tool call.
	FaultEmptyCompletion FaultKind = "empty_completion"
	// FaultCanceled is caller cancellation.
	FaultCanceled FaultKind = "canceled"
	// FaultModelRefused is a structured refusal of one model.
	FaultModelRefused FaultKind = "model_refused"
)

// Fault is the outcome of one provider attempt.
type Fault struct {
	Kind FaultKind
	// Status is the HTTP status, or 0 for a fault that never got one.
	Status int
	// Header carries Retry-After and related headers for the backoff.
	Header http.Header
	// Elapsed includes the silent wait.
	Elapsed time.Duration
	Err     error
}

// Transport reports a retryable response-path fault without an HTTP status.
func (f Fault) Transport() bool {
	return f.Kind == FaultUnreachable || f.Kind == FaultSilent || f.Kind == FaultEmptyCompletion
}

// ClassifyAttempt uses delivery state to classify transport faults.
func ClassifyAttempt(
	ctx context.Context,
	resp *http.Response,
	obs httpclient.RequestObservation,
	elapsed time.Duration,
	err error,
) Fault {
	// Caller cancellation outranks transport state.
	if ctx != nil && ctx.Err() != nil {
		return Fault{Kind: FaultCanceled, Elapsed: elapsed, Err: ctx.Err()}
	}
	if err != nil {
		kind := FaultUnreachable
		if obs.Delivered() {
			kind = FaultSilent
		}
		return Fault{Kind: kind, Elapsed: elapsed, Err: err}
	}
	if resp == nil {
		return Fault{Kind: FaultUnreachable, Elapsed: elapsed, Err: err}
	}
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return Fault{Kind: FaultNone, Status: resp.StatusCode, Header: resp.Header, Elapsed: elapsed}
	}
	return Fault{
		Kind:    faultKindForStatus(resp.StatusCode),
		Status:  resp.StatusCode,
		Header:  resp.Header,
		Elapsed: elapsed,
	}
}

func faultKindForStatus(status int) FaultKind {
	switch status {
	case http.StatusTooManyRequests:
		return FaultRateLimited
	case http.StatusServiceUnavailable, HTTPStatusAnthropicOverloaded:
		return FaultCapacity
	default:
		return FaultStatus
	}
}

// FaultForStatus classifies a typed status without a response.
func FaultForStatus(status int, err error) Fault {
	if status == 0 {
		return Fault{Kind: FaultUnreachable, Err: err}
	}
	return Fault{Kind: faultKindForStatus(status), Status: status, Err: err}
}

func retryReasonForFault(f Fault) RetryReason {
	switch f.Kind {
	case FaultCapacity:
		return RetryReasonCapacity
	case FaultUnreachable:
		return RetryReasonUnreachable
	case FaultSilent:
		return RetryReasonSilent
	case FaultEmptyCompletion:
		return RetryReasonEmptyCompletion
	default:
		return RetryReasonStatus
	}
}
