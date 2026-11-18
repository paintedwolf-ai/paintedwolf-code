//go:build integration

package sessioncontracts

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestPromptBindsAskAndSessionUpgradeIsPermanent(t *testing.T) {
	server := contractfixture.NewTestServerWithWorkflows(t)
	sess := contractfixture.CreateSessionAtPathOnServer(t, server, t.TempDir(), wire.SessionPostureBuild)
	path := "/v1/sessions/" + sess.ID
	prepared := modeSession(t, server, path)
	if prepared.Mode != wire.SessionModeAgent || prepared.Started {
		t.Fatalf("prepared session = %s/%t, want agent/false", prepared.Mode, prepared.Started)
	}
	prompt := fmt.Sprintf(`{"operation_id":%q,"text":"Describe the workspace","mode":"ask"}`, uuid.NewString())
	modeRequest(t, server, http.MethodPost, path+"/prompts", prompt, http.StatusAccepted)
	confirmed := modeSession(t, server, path)
	if confirmed.Mode != wire.SessionModeAsk || !confirmed.Started {
		t.Fatalf("admitted session = %s/%t, want ask/true", confirmed.Mode, confirmed.Started)
	}

	modeRequest(t, server, http.MethodPatch, path, `{"mode":"agent"}`, http.StatusOK)
	modeRequest(t, server, http.MethodPatch, path, `{"mode":"agent"}`, http.StatusOK)
	modeRequest(t, server, http.MethodPatch, path, `{"mode":"ask"}`, http.StatusConflict)
	confirmed = modeSession(t, server, path)
	if confirmed.Mode != wire.SessionModeAgent || !confirmed.Started {
		t.Fatalf("upgraded session = %s/%t, want agent/true", confirmed.Mode, confirmed.Started)
	}
}

func TestWorkflowStartInAskLeavesFirstAdmissionOpen(t *testing.T) {
	server := contractfixture.NewTestServerWithWorkflows(t)
	sess := contractfixture.CreateSessionAtPathOnServer(t, server, t.TempDir(), wire.SessionPostureBuild)
	path := "/v1/sessions/" + sess.ID
	start := fmt.Sprintf(`{"operation_id":%q,"workflow_id":"plan","workflow_version":"1.0.1","mode":"ask"}`, uuid.NewString())
	modeRequest(t, server, http.MethodPost, path+"/workflow-runs", start, http.StatusConflict)
	confirmed := modeSession(t, server, path)
	if confirmed.Mode != wire.SessionModeAgent || confirmed.Started {
		t.Fatalf("refused workflow changed session to %s/%t", confirmed.Mode, confirmed.Started)
	}
}

func TestStandaloneAskAnswersWithoutStartingAmbientWorkflow(t *testing.T) {
	server := contractfixture.NewTestServerWithWorkflows(t)
	project := contractfixture.CreateProjectForTest(t, server, t.TempDir())
	create := fmt.Sprintf(`{"project_id":%q,"posture":"build","mode":"ask"}`, project.ID)
	created := modeRequest(t, server, http.MethodPost, "/v1/sessions", create, http.StatusAccepted)
	var sess wire.Session
	testutil.FailErr(t, "decode Ask session", json.Unmarshal(created.Body.Bytes(), &sess))
	contractfixture.DrainBackground(t, server)

	path := "/v1/sessions/" + sess.ID
	prepared := modeSession(t, server, path)
	if prepared.Status != wire.SessionStatusIdle || prepared.Started || prepared.Mode != wire.SessionModeAsk {
		t.Fatalf("prepared Ask = %s/%t/%s, want idle/false/ask", prepared.Status, prepared.Started, prepared.Mode)
	}
	baseURL := contractfixture.StartTestHTTPServer(t, server)
	contractfixture.AcceptPrompt(t, baseURL, sess.ID, "What is in an empty workspace?")
	_, streamURL := contractfixture.WaitForAssistantStream(t, baseURL, sess.ID)
	content, done := contractfixture.ReadSSEStream(t, baseURL+streamURL)
	if !done || strings.TrimSpace(content) == "" {
		t.Fatalf("Ask response = %q, done = %t", content, done)
	}

	resp := modeRequest(t, server, http.MethodGet, path+"/workflow-runs/active", "", http.StatusOK)
	var active wire.ActiveWorkflowRunResponse
	testutil.FailErr(t, "decode active workflow", json.Unmarshal(resp.Body.Bytes(), &active))
	if active.Run != nil {
		t.Fatalf("standalone Ask started ambient workflow %q", active.Run.WorkflowID)
	}
}

func modeSession(t *testing.T, server *hostapi.Server, path string) wire.Session {
	t.Helper()
	resp := modeRequest(t, server, http.MethodGet, path, "", http.StatusOK)
	var sess wire.Session
	testutil.FailErr(t, "decode session", json.Unmarshal(resp.Body.Bytes(), &sess))
	return sess
}

func modeRequest(t *testing.T, server *hostapi.Server, method, path, body string, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	req := contractfixture.NewAuthedRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != wantStatus {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, resp.Code, wantStatus, resp.Body)
	}
	return resp
}
