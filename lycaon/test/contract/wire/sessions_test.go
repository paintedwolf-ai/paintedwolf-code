package contract

import (
	"net/http"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestContractSessions(t *testing.T) {
	t.Parallel()
	srv := NewStubServer()
	t.Cleanup(srv.Close)
	client := srv.Client()
	base := srv.URL

	var session api.Session
	contractcheck.DoJSON(t, client, http.MethodPost, base+"/v1/sessions", api.CreateSessionRequest{
		ProjectID: fixtureProjectID,
		Posture:   api.SessionPostureSpec,
	}, http.StatusAccepted, &session)
	contractcheck.AssertJSONRoundTrip(t, &session)

	contractcheck.DoJSON(t, client, http.MethodGet, base+"/v1/sessions/"+fixtureSessionID, nil, http.StatusOK, &session)

	var prompt api.PromptAcceptedResponse
	contractcheck.DoJSON(t, client, http.MethodPost, base+"/v1/sessions/"+fixtureSessionID+"/prompts", api.PromptRequest{
		Text: "hello",
	}, http.StatusAccepted, &prompt)

	var aborted api.Session
	contractcheck.DoJSON(t, client, http.MethodPost, base+"/v1/sessions/"+fixtureSessionID+"/abort", api.AbortSessionRequest{
		Reason: "user stopped",
	}, http.StatusOK, &aborted)

	var stopped api.BackgroundProcessStopResult
	contractcheck.DoJSON(t, client, http.MethodPost, base+"/v1/sessions/"+fixtureSessionID+"/background/bg-1/stop", nil, http.StatusOK, &stopped)

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/v1/sessions/"+fixtureSessionID+"/stream", nil)
	resp, err := client.Do(req)
	contractcheck.FailErr(t, "client.Do failed", err)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("stream content-type %q", ct)
	}
}
