package httpaction

import (
	"context"
	"net/url"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Any release review in an exchange records its recipient once the person
// lets the value go there, whatever surface raised it: the hop to that
// recipient then carries the consent, and a withheld value carries none.
func TestEveryAllowedReleaseRecordsItsRecipient(t *testing.T) {
	target, err := url.Parse("https://mtls.example.test/api")
	testutil.FailErr(t, "parse target URL", err)
	id, label := secretmatch.HTTPDestination(target)
	for name, decision := range map[string]secretmatch.Decision{"sent": secretmatch.SendUnchanged, "redacted": secretmatch.SendRedacted, "withheld": secretmatch.Withhold} {
		t.Run(name, func(t *testing.T) {
			var consented bool
			restoreResolver()
			t.Cleanup(restoreResolver)
			confine.SetEgressResolver(func(ctx context.Context, _ confine.EgressCommand, _ egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
				consented = egressgate.RequestConsented(ctx, "mtls.example.test", 443)
				return true
			})
			ctx := egressgate.WithReviewedRecipients(egressgate.WithAttribution(t.Context(), confine.EgressCommand{SessionID: "s", RootSessionID: "s", ToolName: "http_request", ActionID: "a-" + name}))
			ask := recordReviewedRecipient(func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
				return secretmatch.Resolution{Decision: decision}, nil
			})
			_, askErr := ask(ctx, secretmatch.Alert{DestinationID: id, DestinationLabel: label, RuleTitle: "Client certificate"})
			testutil.FailErr(t, "ask release review", askErr)
			hopErr := egressgate.AwaitHTTPHop(ctx, target, "GET", id)
			testutil.FailErr(t, "await HTTP hop", hopErr)
			if want := decision != secretmatch.Withhold; consented != want {
				t.Fatalf("hop consent = %v, want %v", consented, want)
			}
		})
	}
}
