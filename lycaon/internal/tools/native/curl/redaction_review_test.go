package curl

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCurlRedactionReceiptRequestsReviewWithoutSendingTheValue(t *testing.T) {
	const key = "review-fixture-credential-564"
	received := make(chan string, 2)
	server := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Get("X-Session")
		_, _ = w.Write([]byte("ok"))
	})
	h := newHarness(t)
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "create matcher", err)
	fp, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("r", 32)))
	testutil.FailErr(t, "create fingerprinter", err)
	matcher.SetFingerprinter(fp)
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{Name: "fixture", Secret: key, RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle, Source: secretmatch.SourceRememberedMatch}}
	})
	h.owners.HTTP.SecretMatcher = matcher
	const receipt = "d7a148c575bc"
	var asks []secretmatch.Alert
	h.owners.HTTP.SecretAsk = func(_ context.Context, alert secretmatch.Alert) (secretmatch.Resolution, error) {
		asks = append(asks, alert)
		if len(asks) == 1 {
			return secretmatch.Resolution{Decision: secretmatch.SendRedacted, ReceiptToken: receipt}, nil
		}
		return secretmatch.Resolution{Decision: secretmatch.Withhold}, nil
	}
	first := h.curl(t.Context(), "-s", "-H", "X-Session: "+key, server.URL)
	testutil.FailErr(t, "redacted exchange", first.err)
	if !strings.Contains(first.evidence.EvidenceRef, "redaction_receipt:"+receipt) || len(received) != 1 || <-received != secretmatch.RedactedMarker {
		t.Fatal("redacted exchange lost its reviewed bytes or receipt")
	}
	second := h.curl(t.Context(), "-s", "--review-redaction", receipt, "-H", "X-Session: "+key, server.URL)
	if second.err == nil || len(received) != 0 || len(asks) != 2 || asks[0].ContestToken != "" || asks[1].ContestToken != receipt || asks[0].DestinationID != asks[1].DestinationID {
		t.Fatal("contested send did not honor the reviewer's refusal")
	}
}
