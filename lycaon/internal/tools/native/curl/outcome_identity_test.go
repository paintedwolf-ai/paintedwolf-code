package curl

import (
	"testing"

	"github.com/lycaon/lycaon/internal/outboundhttp"
)

func TestRedirectLimitOutcomeUsesTypedFact(t *testing.T) {
	for _, test := range []struct {
		name    string
		failure *outboundhttp.RedirectError
		want    int
	}{
		{"typed limit", &outboundhttp.RedirectError{LimitExceeded: true, Reason: "diagnostic changed"}, 47},
		{"untrusted prose", &outboundhttp.RedirectError{Reason: "too many redirects"}, 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, _ := TransportOutcome(test.failure)
			if code != test.want {
				t.Fatalf("redirect status=%d; want%d", code, test.want)
			}
		})
	}
}
