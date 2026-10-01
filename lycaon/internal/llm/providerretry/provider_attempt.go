// Package providerretry coordinates classified HTTP retries, shared rate admission, and transport failure records.
package providerretry

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/lycaon/lycaon/internal/httpclient"
)

const providerErrorBodyLimit = 1 << 20

// ProviderAttempt is one reissuable provider request.
type ProviderAttempt struct {
	// ProviderID names the instance for the exhausted error.
	ProviderID string
	// Model names the requested model.
	Model string
	// RefusalCodes identify structured model refusals.
	RefusalCodes []string
	Policy       ProviderHTTPRetry
	// Send rebuilds the request with this context.
	Send func(ctx context.Context) (*http.Response, error)
	// Describe formats structured response failures.
	Describe func(status int, body []byte) error
}

// RunProviderAttempts returns the first success or final fault.
func RunProviderAttempts(ctx context.Context, a ProviderAttempt) (*http.Response, error) {
	var lastBody []byte
	retries := 0
	for attempt := 0; ; attempt++ {
		admission, err := a.Admission(ctx)
		if err != nil {
			return nil, err
		}
		obsCtx, readObs := httpclient.Observe(ctx)
		started := time.Now()
		resp, err := a.Send(obsCtx)
		fault := ClassifyAttempt(ctx, resp, readObs(), time.Since(started), err)
		if fault.Kind == FaultNone {
			if err := admission.Succeeded(ctx); err != nil {
				_ = resp.Body.Close()
				return nil, err
			}
			return resp, nil
		}
		if resp != nil {
			lastBody, _ = io.ReadAll(io.LimitReader(resp.Body, providerErrorBodyLimit))
			_ = resp.Body.Close()
			fault = a.refineFault(fault, lastBody)
		}
		if fault.Kind == FaultCanceled {
			return nil, fault.Err
		}
		// A pair-specific refusal is not retryable.
		if fault.Kind == FaultModelRefused {
			return nil, a.refused(fault, lastBody)
		}
		if fault.Kind == FaultRateLimited && admission != nil {
			if err := admission.Limited(ctx, fault.Header, a.Policy); err != nil {
				return nil, err
			}
			if admission.policy.Adaptive {
				continue
			}
		}
		if !ShouldRetryFault(a.Policy, retries, fault) {
			return nil, a.exhausted(fault, lastBody, attempt+1)
		}
		delay := WaitForFault(a.Policy, retries, fault)
		if a.Policy.waitsForAvailability(fault) && admission != nil {
			if err := admission.unavailable(ctx, delay); err != nil {
				return nil, err
			}
		}
		if waitErr := awaitFaultRetry(ctx, a.Policy, retries, fault, delay); waitErr != nil {
			return nil, waitErr
		}
		retries++
	}
}

// refineFault reads only structured refusal fields.
func (a ProviderAttempt) refineFault(fault Fault, body []byte) Fault {
	if fault.Kind != FaultStatus || len(a.RefusalCodes) == 0 {
		return fault
	}
	parsed, ok := ParseOpenAICompatError(body)
	if !ok || ModelRefusalEvidenceFor(fault.Status, parsed, a.RefusalCodes) == RefusalEvidenceNone {
		return fault
	}
	fault.Kind = FaultModelRefused
	return fault
}

func (a ProviderAttempt) refused(fault Fault, body []byte) error {
	parsed, _ := ParseOpenAICompatError(body)
	detail := ""
	if a.Describe != nil {
		if described := a.Describe(fault.Status, body); described != nil {
			detail = described.Error()
		}
	}
	return &ModelRefusedError{
		ProviderID: a.ProviderID,
		Model:      a.Model,
		Status:     fault.Status,
		Code:       parsed.Code,
		Detail:     detail,
		Evidence:   ModelRefusalEvidenceFor(fault.Status, parsed, a.RefusalCodes),
	}
}

func (a ProviderAttempt) exhausted(fault Fault, body []byte, attempts int) error {
	if fault.Transport() {
		return TransportFaultError(a.ProviderID, a.Model, fault, attempts)
	}
	var formatted error
	if a.Describe != nil {
		formatted = a.Describe(fault.Status, body)
	}
	err := ExhaustedHTTPError(a.ProviderID, a.Model, fault.Status, attempts, formatted)
	if rejected, ok := AsProviderRequestRejected(err); ok {
		rejected.responseBody = append([]byte(nil), body...)
	}
	return err
}
