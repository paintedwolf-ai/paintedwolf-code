package providerretry

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/lycaon/lycaon/internal/llm/failure"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// HTTP 529 indicates overload.
const HTTPStatusAnthropicOverloaded = 529

// TransportFaultError names a spent transport budget.
func TransportFaultError(providerID, model string, fault Fault, attempts int) error {
	if fault.Kind == FaultEmptyCompletion {
		if original, ok := failure.AsProviderEmptyCompletion(fault.Err); ok {
			cloned := *original
			cloned.Attempts = attempts
			return &cloned
		}
		return &failure.ProviderEmptyCompletionError{
			ProviderID: providerID,
			Model:      model,
			Retryable:  true,
			Attempts:   attempts,
		}
	}
	if fault.Kind == FaultSilent {
		return &failure.ProviderSilentError{
			ProviderID: providerID,
			Model:      model,
			Attempts:   attempts,
			Silence:    fault.Elapsed,
			Cause:      fault.Err,
		}
	}
	return &failure.ProviderUnreachableError{ProviderID: providerID, Model: model, Attempts: attempts, Cause: fault.Err}
}

// ExhaustedHTTPError classifies a spent retry budget by status.
func ExhaustedHTTPError(providerID, model string, status, attempts int, formatted error) error {
	if formatted == nil {
		formatted = fmt.Errorf("provider %s: HTTP %d", providerID, status)
	}
	switch status {
	case 429:
		return &failure.ProviderRateLimitedError{
			ProviderID: providerID,
			Model:      model,
			Status:     status,
			Attempts:   attempts,
			Detail:     formatted.Error(),
		}
	case 503, HTTPStatusAnthropicOverloaded:
		return &failure.ProviderOverloadedError{
			ProviderID: providerID,
			Model:      model,
			Status:     status,
			Attempts:   attempts,
			Detail:     formatted.Error(),
		}
	default:
		if status >= http.StatusBadRequest && status <= 499 {
			return &ProviderRequestRejectedError{ProviderID: providerID, Model: model, Status: status, Cause: formatted}
		}
		if status >= http.StatusInternalServerError && status <= 599 {
			return &failure.ProviderServerError{
				ProviderID: providerID,
				Model:      model,
				Status:     status,
				Attempts:   attempts,
				Detail:     formatted.Error(),
			}
		}
		return formatted
	}
}

// ProviderRejectionReason names a cause established by a provider's protocol.
type ProviderRejectionReason string

// RejectionCloudflareWorkersPaidRequired identifies Cloudflare error 5035.
const RejectionCloudflareWorkersPaidRequired ProviderRejectionReason = "cloudflare_workers_paid_required"

// ProviderRequestRejectedError preserves a terminal HTTP request rejection.
// Status alone does not identify authentication, content filtering, or bad input.
type ProviderRequestRejectedError struct {
	ProviderID string
	Model      string
	Status     int
	Reason     ProviderRejectionReason
	Cause      error
	// responseBody is bounded HTTP data for catalog-defined structured codes.
	// Keep it out of serialized diagnostics and user-facing notice context.
	responseBody []byte
}

func (e *ProviderRequestRejectedError) Error() string {
	if e.Cause == nil {
		return fmt.Sprintf("provider %s rejected the request (HTTP %d)", e.ProviderID, e.Status)
	}
	return fmt.Sprintf("provider %s rejected the request (HTTP %d): %v", e.ProviderID, e.Status, e.Cause)
}

func (e *ProviderRequestRejectedError) Unwrap() error { return e.Cause }

func (e *ProviderRequestRejectedError) NoticeCode() wire.NoticeCode {
	return wire.NoticeCodeProviderRequestRejected
}

// AsProviderRequestRejected extracts a terminal provider request rejection.
func AsProviderRequestRejected(err error) (*ProviderRequestRejectedError, bool) {
	var rejected *ProviderRequestRejectedError
	ok := errors.As(err, &rejected)
	return rejected, ok
}

// ResponseBody exposes the bounded protocol data to the catalog classifier.
func (e *ProviderRequestRejectedError) ResponseBody() []byte { return e.responseBody }
