package promptadmin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestPromptModeRefusalsPreserveFirstAdmission(t *testing.T) {
	routes := newSessionRoutes(t)
	ask := wire.SessionModeAsk
	post := func(t *testing.T, text string, want wire.ApiErrorCode, status int) {
		t.Helper()
		id := uuid.NewString()
		body, err := json.Marshal(wire.PromptRequest{OperationID: id, Mode: &ask, Text: text})
		testutil.FailErr(t, "encode prompt", err)
		response := routes.serve(t, http.MethodPost, "/v1/sessions/"+routes.sessionID+"/prompts", string(body))
		if response.Code != status || responseCode(t, response) != want {
			t.Fatalf("prompt response = %d %s, want %d %q", response.Code, response.Body.String(), status, want)
		}
		if _, err := routes.store.GetPromptSubmission(t.Context(), id); !errors.Is(err, store.ErrPromptSubmissionNotFound) {
			t.Fatalf("refused prompt has a durable submission: %v", err)
		}
	}
	post(t, "", wire.ApiErrorCodeInvalidRequest, http.StatusBadRequest)
	root, err := routes.store.Get(t.Context(), routes.sessionID)
	testutil.FailErr(t, "read chat", err)
	if root.Started || root.Mode != wire.SessionModeAgent {
		t.Fatalf("empty Ask changed the chat: mode=%s started=%v", root.Mode, root.Started)
	}

	id := uuid.NewString()
	_, _, err = routes.store.PutPromptSubmission(t.Context(), store.PromptSubmission{
		ID: id, SessionID: root.ID, ProjectID: root.ProjectID, SubmittedBy: root.OwnerPersonID,
		InputDigest: id, InputJSON: "{}", Origin: store.PromptSubmissionOriginUser,
	})
	testutil.FailErr(t, "start Agent chat", err)
	post(t, "Inspect the change", wire.ApiErrorCodeSessionModeLocked, http.StatusConflict)
}

func TestPromptModeNeedsDurableRootAuthority(t *testing.T) {
	routes := newSessionRoutes(t)
	root, err := routes.store.Get(t.Context(), routes.sessionID)
	testutil.FailErr(t, "read root chat", err)
	child, err := routes.store.CreateChild(t.Context(), root, wire.SpawnChildRequest{AgentType: "repo-researcher"})
	testutil.FailErr(t, "create worker chat", err)
	ask := wire.SessionModeAsk
	for _, tc := range []struct {
		sessionID string
		status    int
		code      wire.ApiErrorCode
	}{
		{"missing", http.StatusServiceUnavailable, wire.ApiErrorCodeSessionModeUnavailable},
		{child.ID, http.StatusBadRequest, wire.ApiErrorCodeInvalidRequest},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		allowEmpty, ok := routes.submission.promptModeForAdmission(w, r, tc.sessionID, &ask)
		if ok || allowEmpty || w.Code != tc.status || responseCode(t, w) != tc.code {
			t.Fatalf("mode check = %d %s, allow empty=%v, accepted=%v", w.Code, w.Body.String(), allowEmpty, ok)
		}
	}
}

func TestQueueModeIntentIsOnlyAvailableForRootSends(t *testing.T) {
	routes := newSessionRoutes(t)
	root, err := routes.store.SetMode(t.Context(), routes.sessionID, wire.SessionModeAsk)
	testutil.FailErr(t, "set root mode", err)
	child, err := routes.store.CreateChild(t.Context(), root, wire.SpawnChildRequest{AgentType: "repo-researcher"})
	testutil.FailErr(t, "create worker chat", err)
	for _, tc := range []struct{ sessionID, body string }{
		{root.ID, `{"op":"send","mode":"ask","expected_revision":0}`},
		{root.ID, `{"op":"hold","mode":"agent","hold":true,"expected_revision":0}`},
		{child.ID, `{"op":"send","mode":"agent","expected_revision":0}`},
	} {
		w := routes.serve(t, http.MethodPatch, "/v1/sessions/"+tc.sessionID+"/queue", tc.body)
		if w.Code != http.StatusBadRequest || responseCode(t, w) != wire.ApiErrorCodeInvalidRequest {
			t.Fatalf("queue mode mutation = %d %s", w.Code, w.Body.String())
		}
	}
	authority, err := routes.store.ResolveEffectiveMode(t.Context(), root.ID)
	testutil.FailErr(t, "read chat authority", err)
	if authority.Mode != wire.SessionModeAsk || authority.Started {
		t.Fatalf("rejected requests changed chat authority: %+v", authority)
	}
}
