package security

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestPromptWithoutProviderPublishesRenderedHostError(t *testing.T) {

	_, projectDir := noMockServeEnv(t)
	serveApp := buildNoMockServeApp(t)
	httpSrv := httptest.NewServer(serveApp.Server)
	t.Cleanup(httpSrv.Close)
	base := httpSrv.URL

	project := createAPIProjectAtPath(t, base, projectDir)
	sess := openAPIPostJSON[wire.Session](t, base, "/v1/sessions", nil,
		`{"project_id":"`+project.ID+`","posture":"build"}`, http.StatusAccepted)

	subCtx, subCancel := context.WithCancel(t.Context())
	defer subCancel()
	hub := newProjectHub(t, subCtx, base, project.ID)

	acceptPromptOpenAPI(t, base, sess.ID, `{"text":"hello"}`)
	waitPromptIdleHTTP(t, base, sess.ID)

	env := waitForSessionHostError(t, hub)
	var sessionEv wire.SessionEvent
	if err := json.Unmarshal(env.Data, &sessionEv); err != nil {
		t.Fatalf("decode session event: %v", err)
	}
	if sessionEv.HostError == nil {
		t.Fatalf("session envelope missing host_error: %+v", sessionEv)
	}
	host := *sessionEv.HostError
	if host.Code != "provider_not_configured" {
		t.Fatalf("host_error code = %q want provider_not_configured", host.Code)
	}
	assertUserFacingHostError(t, host)
}

func waitForSessionHostError(t *testing.T, hub *projectHub) wire.EventEnvelope {
	t.Helper()
	for t.Context().Err() == nil {
		env := hub.waitFor(t, wire.EventTopicSession)
		var raw map[string]any
		if err := json.Unmarshal(env.Data, &raw); err != nil {
			t.Fatalf("session data not an object: %v", err)
		}
		if _, ok := raw["host_error"]; ok {
			return env
		}
	}
	t.Fatal("timeout waiting for session envelope with host_error")
	return wire.EventEnvelope{}
}

func waitPromptIdleHTTP(t *testing.T, base, sessionID string) {
	t.Helper()
	testutil.WaitFor(t, func() bool {
		sess := openAPIGetJSON[wire.Session](t, base, "/v1/sessions/{id}",
			map[string]string{"id": sessionID}, http.StatusOK)
		return sess.Status == wire.SessionStatusIdle
	})
}
