package llm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/providerretry"
)

func TestProviderRejectionRuleYAML(t *testing.T) {
	cfg, err := decodeProviderConfig([]byte(`providers:
  - id: any-provider
    rejection_reasons:
      - status: 403
        code_path: error.type
        code: payment_required
        reason: custom_payment_required
`))
	if err != nil {
		t.Fatalf("decode rejection rules: %v", err)
	}
	entry, err := resolveLocalEntry(cfg.Providers[0], nil)
	if err != nil {
		t.Fatalf("resolve rejection rules: %v", err)
	}
	// The common HTTP attempt retains structured data for every HTTP adapter.
	_, failure := providerretry.RunProviderAttempts(t.Context(), providerretry.ProviderAttempt{ //nolint:bodyclose // A failed attempt returns no response.
		ProviderID: entry.ID,
		Send: func(context.Context) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{},
				Body: io.NopCloser(strings.NewReader(`{"error":{"type":"payment_required","message":"private diagnostics"}}`))}, nil
		},
	})
	got, ok := providerretry.AsProviderRequestRejected(rejectionReason(failure, entry.RejectionReasons))
	if !ok || got.Reason != "custom_payment_required" {
		t.Fatalf("custom provider mapping was not applied: %v", got)
	}
	copy := cloneCatalogEntry(entry)
	copy.RejectionReasons[0].Code = "changed"
	if entry.RejectionReasons[0].Code != "payment_required" {
		t.Fatal("catalog clone shared mutable rejection rules")
	}
}

func TestProviderRejectionRuleValidation(t *testing.T) {
	for _, rule := range []ProviderRejectionRule{
		{Status: 500, CodePath: "error.code", Code: "x", Reason: "x"},
		{Status: 403, CodePath: "error..code", Code: "x", Reason: "x"},
		{Status: 403, CodePath: "error.code", Reason: "x"},
	} {
		if validateRejectionRules([]ProviderRejectionRule{rule}) == nil {
			t.Fatalf("invalid rejection rule accepted: %+v", rule)
		}
	}
}
