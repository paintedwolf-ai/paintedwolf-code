package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
)

// Only an explicit refusal denies a model; most hosts publish no callability
// evidence, so unknown passes.
func TestCapabilityRefusedPassesUnknown(t *testing.T) {
	cases := []struct {
		name    string
		state   modelinfo.CapabilityState
		refused bool
	}{
		{"unset", "", false},
		{"unknown", modelinfo.CapabilityUnknown, false},
		{"supported", modelinfo.CapabilitySupported, false},
		{"unsupported", modelinfo.CapabilityUnsupported, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := modelinfo.Refused(modelinfo.CapabilityEvidence{State: tc.state})
			if got != tc.refused {
				t.Fatalf("capabilityRefused(%q) = %v, want %v", tc.state, got, tc.refused)
			}
		})
	}
}

func TestPartitionRefusedModelsKeepsUnknownAssignable(t *testing.T) {
	models := []modelinfo.Entry{
		{ID: "plain"},
		{ID: "known-good", Callable: modelinfo.Evidence(modelinfo.CapabilitySupported, "provider-listing")},
		{ID: "declined", Callable: modelinfo.Evidence(modelinfo.CapabilityUnsupported, "provider-listing")},
	}
	assignable, refused := partitionRefusedModels(models)
	if len(assignable) != 2 || assignable[0].ID != "plain" || assignable[1].ID != "known-good" {
		t.Fatalf("assignable = %v, want [plain known-good]", modelIDs(assignable))
	}
	if len(refused) != 1 || refused[0].ID != "declined" {
		t.Fatalf("refused = %v, want [declined]", modelIDs(refused))
	}
}

func modelIDs(models []modelinfo.Entry) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		out = append(out, m.ID)
	}
	return out
}

func TestModelRefusedReadsStructuredFieldsOnly(t *testing.T) {
	declared := providerretry.DefaultModelRefusalCodes()
	cases := []struct {
		name   string
		status int
		parsed providerretry.ParsedProviderError
		want   providerretry.ModelRefusalEvidence
	}{
		{
			name:   "code match",
			status: http.StatusBadRequest,
			parsed: providerretry.ParsedProviderError{Code: "model_not_available"},
			want:   providerretry.RefusalEvidenceModelIdentity,
		},
		{
			// The provider pointed at the model field but used no declared code.
			// That separates the model from max_tokens; it does not separate "no
			// such model" from "this model will not serve this request".
			name:   "param alone is inconclusive evidence",
			status: http.StatusBadRequest,
			parsed: providerretry.ParsedProviderError{Param: "model", Code: "invalid_request_error"},
			want:   providerretry.RefusalEvidenceInconclusive,
		},
		{
			name:   "type carries the token",
			status: http.StatusNotFound,
			parsed: providerretry.ParsedProviderError{Type: "model_not_found"},
			want:   providerretry.RefusalEvidenceModelIdentity,
		},
		{
			name:   "status carries the token",
			status: http.StatusNotFound,
			parsed: providerretry.ParsedProviderError{Code: "404", Status: "unknown_model"},
			want:   providerretry.RefusalEvidenceModelIdentity,
		},
		{
			// Prose that describes a refusal is not a refusal.
			name:   "message alone is never enough",
			status: http.StatusBadRequest,
			parsed: providerretry.ParsedProviderError{Message: "Unable to access non-serverless model Foo/Bar"},
			want:   providerretry.RefusalEvidenceNone,
		},
		{
			name:   "server faults cannot refuse",
			status: http.StatusInternalServerError,
			parsed: providerretry.ParsedProviderError{Code: "model_not_found"},
			want:   providerretry.RefusalEvidenceNone,
		},
		{
			name:   "unrelated request fault",
			status: http.StatusBadRequest,
			parsed: providerretry.ParsedProviderError{Code: "context_length_exceeded", Param: "messages"},
			want:   providerretry.RefusalEvidenceNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := providerretry.ModelRefusalEvidenceFor(tc.status, tc.parsed, declared); got != tc.want {
				t.Fatalf("modelRefusalEvidence = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestModelRefusalCodesFallBackToDefaults(t *testing.T) {
	if got := providerprofile.ModelRefusalCodesFor(providerprofile.Profile{}); len(got) == 0 {
		t.Fatal("empty profile must fall back to the shared default codes")
	}
	custom := providerprofile.Profile{ModelRefusalCodes: []string{"NOT_FOUND"}}
	got := providerprofile.ModelRefusalCodesFor(custom)
	if len(got) != 1 || got[0] != "NOT_FOUND" {
		t.Fatalf("declared codes = %v, want [NOT_FOUND]", got)
	}
}

func TestRefusalIsNotRetried(t *testing.T) {
	body := `{"error":{"message":"Unable to access non-serverless model Foo/Bar","type":"invalid_request_error","code":"model_not_available"}}`
	attempts := 0
	resp, err := providerretry.RunProviderAttempts(context.Background(), providerretry.ProviderAttempt{
		ProviderID:   "together-1",
		Model:        "Foo/Bar",
		RefusalCodes: providerretry.DefaultModelRefusalCodes(),
		Policy: providerretry.ProviderHTTPRetry{
			MaxRetries:  3,
			MaxWaitMs:   1,
			BackoffMs:   []int{1, 1, 1},
			Statuses:    []int{400},
			WaitHeaders: []string{"Retry-After"},
		},
		Send: func(context.Context) (*http.Response, error) {
			attempts++
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{},
			}, nil
		},
	})
	if resp != nil {
		_ = resp.Body.Close()
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1: a refusal must not be reissued", attempts)
	}
	refusal, ok := providerretry.AsModelRefused(err)
	if !ok {
		t.Fatalf("err = %v, want a ModelRefusedError", err)
	}
	if refusal.ProviderID != "together-1" || refusal.Model != "Foo/Bar" {
		t.Fatalf("refusal names %q/%q, want together-1/Foo/Bar", refusal.ProviderID, refusal.Model)
	}
	if refusal.Code != "model_not_available" {
		t.Fatalf("refusal code = %q, want model_not_available", refusal.Code)
	}
	if !errors.Is(err, providerretry.ErrModelRefused) {
		t.Fatal("refusal must match ErrModelRefused")
	}
}

func TestRetryableStatusStillRetries(t *testing.T) {
	attempts := 0
	resp, _ := providerretry.RunProviderAttempts(context.Background(), providerretry.ProviderAttempt{
		ProviderID:   "together-1",
		Model:        "Foo/Bar",
		RefusalCodes: providerretry.DefaultModelRefusalCodes(),
		Policy: providerretry.ProviderHTTPRetry{
			MaxRetries:  2,
			MaxWaitMs:   1,
			BackoffMs:   []int{1, 1},
			Statuses:    []int{400},
			WaitHeaders: []string{"Retry-After"},
		},
		Send: func(context.Context) (*http.Response, error) {
			attempts++
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"bad","code":"invalid_request_error"}}`)),
				Header:     http.Header{},
			}, nil
		},
	})
	if resp != nil {
		_ = resp.Body.Close()
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3 (initial + 2 retries)", attempts)
	}
}

func TestModelRefusalGateRecordsOnlyRefusals(t *testing.T) {
	gate := providerretry.NewModelRefusalGate()
	gate.Note("p", "m", errors.New("some other failure"))
	if _, ok := gate.Refused("p", "m"); ok {
		t.Fatal("a generic error must not record a refusal")
	}
	gate.Note("p", "m", &providerretry.ModelRefusedError{ProviderID: "p", Model: "m", Status: 400, Code: "model_not_found", Evidence: providerretry.RefusalEvidenceModelIdentity})
	refusal, ok := gate.Refused("p", "m")
	if !ok {
		t.Fatal("refusal was not recorded")
	}
	if refusal.Code != "model_not_found" {
		t.Fatalf("code = %q, want model_not_found", refusal.Code)
	}
	if err := gate.Err("p", "m"); err == nil {
		t.Fatal("Err must return the standing refusal")
	}
	if err := gate.Err("p", "other"); err != nil {
		t.Fatalf("Err for an unrecorded pair = %v, want nil", err)
	}
	gate.Reset()
	if _, ok := gate.Refused("p", "m"); ok {
		t.Fatal("Reset must retire standing refusals")
	}
}

// TestNilRefusalGateAllows keeps the zero value usable: a route with no gate
// wired must not start denying models.
func TestNilRefusalGateAllows(t *testing.T) {
	var gate *providerretry.ModelRefusalGate
	gate.Note("p", "m", &providerretry.ModelRefusedError{ProviderID: "p", Model: "m", Evidence: providerretry.RefusalEvidenceModelIdentity})
	if err := gate.Err("p", "m"); err != nil {
		t.Fatalf("nil gate returned %v, want nil", err)
	}
}

// A refusal that only points at the model field fails the turn and is not
// remembered: an unsupported request shape reports the same way as an unknown
// model, and the gate is shared with the coordinator's chat calls.
func TestRequestFieldRefusalIsNotRemembered(t *testing.T) {
	gate := providerretry.NewModelRefusalGate()
	gate.Note("p", "m", &providerretry.ModelRefusedError{
		ProviderID: "p",
		Model:      "m",
		Status:     400,
		Code:       "invalid_request_error",
		Evidence:   providerretry.RefusalEvidenceInconclusive,
	})
	if err := gate.Err("p", "m"); err != nil {
		t.Fatalf("inconclusive evidence was remembered: %v", err)
	}
}

// A completed call contradicts a standing refusal, so the gate drops it.
func TestSuccessRetiresStandingRefusal(t *testing.T) {
	gate := providerretry.NewModelRefusalGate()
	gate.Note("p", "m", &providerretry.ModelRefusedError{
		ProviderID: "p",
		Model:      "m",
		Status:     404,
		Code:       "model_not_found",
		Evidence:   providerretry.RefusalEvidenceModelIdentity,
	})
	if err := gate.Err("p", "m"); err == nil {
		t.Fatal("identity evidence must be remembered")
	}
	gate.Clear("p", "m")
	if err := gate.Err("p", "m"); err != nil {
		t.Fatalf("Clear left a standing refusal: %v", err)
	}
}
