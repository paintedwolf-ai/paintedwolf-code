package httpaction

import (
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretcap"
)

func TestHTTPFailureClassificationUsesTypedCauses(t *testing.T) {
	for _, tt := range []struct {
		name      string
		cause     error
		code      string
		retryable bool
	}{
		{"destination denied", &egress.DestinationDeniedError{Reason: "blocked fixture"}, "HTTP_REQUEST_HOST_DENIED", false},
		{"same prose without denial type", errors.New("blocked fixture"), "HTTP_REQUEST_FAILED", true},
		{"invalid request", &outboundhttp.RequestInvalidError{Reason: "invalid fixture"}, "HTTP_REQUEST_FAILED", false},
		{"body bound", &outboundhttp.BodyTooLargeError{Limit: 1024}, "HTTP_REQUEST_FAILED", false},
		{"transient transfer", &outboundhttp.TransferError{Err: errors.New("connection ended")}, "HTTP_REQUEST_FAILED", true},
		{"tls expired", &outboundhttp.TLSFault{Type: "certificate_expired", Detail: "expired", Host: "example.com"}, "HTTP_REQUEST_FAILED", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, cause := range []error{tt.cause, fmt.Errorf("wrapped exchange: %w", tt.cause)} {
				got := sendReject(t.Context(), Deps{}, secretcap.CookieJarRequest{}, nil, secretcap.TokenJarRequest{}, nil, cause)
				if got.Code != tt.code || got.Retryable != tt.retryable || got.Data["reason"] == "" {
					t.Fatalf("reject=%+v", got)
				}
				if got.Code == "HTTP_REQUEST_FAILED" && got.Data["retryable"] != got.Retryable {
					t.Fatalf("retry guidance disagrees with typed outcome: %+v", got)
				}
				var tlsFault *outboundhttp.TLSFault
				if errors.As(tt.cause, &tlsFault) {
					if got.Data["tls_fault"] != tlsFault.Type || got.Data["tls_detail"] != tlsFault.Detail {
						t.Fatalf("tls fault not attached: %+v", got)
					}
				}
			}
		})
	}
}
