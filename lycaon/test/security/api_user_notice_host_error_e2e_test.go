package security

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestPromptWithoutProviderPublishesRenderedHostError(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	t.Cleanup(func() { project.SetDefaultOpenPolicy(project.DefaultOpenPolicy()) })

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
	waitPromptIdleHTTP(t, base, sess.ID, 10*time.Second)

	env := waitForSessionHostError(t, hub, 10*time.Second)
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
	if !strings.Contains(host.Message, "language model") && !strings.Contains(host.Message, "Settings") {
		t.Fatalf("host_error message = %q want user-facing provider copy", host.Message)
	}
}

func waitForSessionHostError(t *testing.T, hub *projectHub, deadline time.Duration) wire.EventEnvelope {
	t.Helper()
	deadlineAt := time.Now().Add(deadline)
	for {
		remaining := time.Until(deadlineAt)
		if remaining <= 0 {
			t.Fatal("timeout waiting for session envelope with host_error")
		}
		env := hub.waitFor(t, wire.EventTopicSession, remaining)
		var raw map[string]any
		if err := json.Unmarshal(env.Data, &raw); err != nil {
			t.Fatalf("session data not an object: %v", err)
		}
		if _, ok := raw["host_error"]; ok {
			return env
		}
	}
}

func waitPromptIdleHTTP(t *testing.T, base, sessionID string, timeout time.Duration) {
	t.Helper()
	var status wire.SessionStatus
	testutil.WaitFor(t, timeout, func() bool {
		sess := openAPIGetJSON[wire.Session](t, base, "/v1/sessions/{id}",
			map[string]string{"id": sessionID}, http.StatusOK)
		status = sess.Status
		return status == wire.SessionStatusIdle
	})
}
