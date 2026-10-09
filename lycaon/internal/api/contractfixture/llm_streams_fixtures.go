package contractfixture

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func GetSessionMessages(t *testing.T, baseURL, sessionID string) []wire.Message {
	t.Helper()
	resp, err := AuthedHTTPGet(baseURL + "/v1/sessions/" + sessionID + "/messages")
	testutil.FailErr(t, "authedHTTPGet failed", err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET messages status = %d", resp.StatusCode)
	}
	var page wire.SessionTranscriptPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	msgs := page.Messages
	return msgs
}

const MinimalShipHTTPRetryYAML = `    http_retry:
      max_retries: 1
      max_wait_ms: 1000
      backoff_ms: [1]
      statuses: [429]
      wait_headers: [Retry-After]
`
