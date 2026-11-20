package contract

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/wiring"
)

// A session is created only with a known posture: a missing or unknown posture
// is refused with the typed request code and creates no session.
func TestCreateSessionRequiresPostureField(t *testing.T) {
	h := wiring.BuildForTest(t)
	baseURL := contractfixture.StartTestHTTPServer(t, h.Server)

	projResp, err := contractfixture.AuthedHTTPPost(baseURL+"/v1/projects", "application/json",
		fmt.Sprintf(`{"roots":[{"path":%q}]}`, t.TempDir()))
	contractcheck.FailErr(t, "create project", err)
	defer func() { _ = projResp.Body.Close() }()
	if projResp.StatusCode != http.StatusCreated {
		t.Fatalf("create project status = %d body = %s", projResp.StatusCode, contractfixture.ReadBody(t, projResp))
	}
	var proj wire.Project
	testutil.FailErr(t, "decode project", json.NewDecoder(projResp.Body).Decode(&proj))

	for _, tc := range []struct {
		name string
		body string
	}{
		{"missing posture", fmt.Sprintf(`{"project_id":%q}`, proj.ID)},
		{"empty posture", fmt.Sprintf(`{"project_id":%q,"posture":""}`, proj.ID)},
		{"unknown posture", fmt.Sprintf(`{"project_id":%q,"posture":"session"}`, proj.ID)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := contractfixture.AuthedHTTPPost(baseURL+"/v1/sessions", "application/json", tc.body)
			contractcheck.FailErr(t, "post session", err)
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", resp.StatusCode, contractfixture.ReadBody(t, resp))
			}
			var refusal wire.ErrorResponse
			testutil.FailErr(t, "decode refusal", json.NewDecoder(resp.Body).Decode(&refusal))
			if refusal.Code != wire.ApiErrorCodeInvalidRequest {
				t.Fatalf("code = %q, want %q", refusal.Code, wire.ApiErrorCodeInvalidRequest)
			}
		})
	}
	if sessions := listProjectSessions(t, baseURL, proj.ID); len(sessions) != 0 {
		t.Fatalf("refused requests created sessions: %+v", sessions)
	}

	// The same request with a known posture is admitted.
	resp, err := contractfixture.AuthedHTTPPost(baseURL+"/v1/sessions", "application/json",
		fmt.Sprintf(`{"project_id":%q,"posture":%q}`, proj.ID, wire.SessionPostureBuild))
	contractcheck.FailErr(t, "post session with posture", err)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", resp.StatusCode, contractfixture.ReadBody(t, resp))
	}
	var sess wire.Session
	testutil.FailErr(t, "decode session", json.NewDecoder(resp.Body).Decode(&sess))
	if sess.Posture != wire.SessionPostureBuild {
		t.Fatalf("posture = %q, want %q", sess.Posture, wire.SessionPostureBuild)
	}
	if sessions := listProjectSessions(t, baseURL, proj.ID); len(sessions) != 1 || sessions[0].ID != sess.ID {
		t.Fatalf("sessions after admission = %+v, want only %s", sessions, sess.ID)
	}
}

func listProjectSessions(t *testing.T, baseURL, projectID string) []wire.SessionSummary {
	t.Helper()
	resp, err := contractfixture.AuthedHTTPGet(baseURL + "/v1/projects/" + projectID + "/sessions")
	contractcheck.FailErr(t, "list sessions", err)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list sessions status = %d body = %s", resp.StatusCode, contractfixture.ReadBody(t, resp))
	}
	var page wire.SessionListPage
	testutil.FailErr(t, "decode session page", json.NewDecoder(resp.Body).Decode(&page))
	return page.Sessions
}
