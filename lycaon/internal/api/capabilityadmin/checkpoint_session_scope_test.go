package capabilityadmin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestCheckpointResolutionStaysWithinItsSession(t *testing.T) {
	server, checkpoints, session := contractfixture.NewCheckpointHandlerFixture(t)
	pending, err := contractfixture.RequestExplicitAPIApprovalCheckpoint(t, checkpoints, hitl.CheckpointRequest{
		SessionID: session.ID, Kind: wire.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{Invocation: hitl.ActionInvocation{Tool: "command"}},
	})
	testutil.FailErr(t, "open checkpoint", err)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := contractfixture.NewAuthedRequest(method, "/v1/sessions/"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		server.ServeHTTP(resp, req)
		return resp
	}

	unknown := uuid.NewString()
	resolve := "/checkpoints/" + pending.CheckpointID
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/checkpoints", ""},
		{http.MethodPost, resolve, `{"kind":"tool_approval","action":"reject"}`},
		{http.MethodPost, resolve + "/unlock-challenges", `{"window_label":"main","option_id":"approve_current_action"}`},
		{http.MethodGet, "/invocations", ""},
		{http.MethodGet, "/bootstrap", ""},
	} {
		resp := request(tc.method, unknown+tc.path, tc.body)
		var failure wire.ErrorResponse
		testutil.FailErr(t, "decode refusal", json.Unmarshal(resp.Body.Bytes(), &failure))
		if resp.Code != http.StatusNotFound || failure.Code != wire.ApiErrorCodeSessionNotFound {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, resp.Code, resp.Body.String())
		}
	}
	list := func() []wire.CheckpointEvent {
		resp := request(http.MethodGet, session.ID+"/checkpoints?kind=tool_approval", "")
		var result wire.CheckpointListResponse
		testutil.FailErr(t, "decode pending checkpoints", json.Unmarshal(resp.Body.Bytes(), &result))
		if resp.Code != http.StatusOK {
			t.Fatalf("list: %d %s", resp.Code, resp.Body.String())
		}
		return result.Checkpoints
	}
	if got := list(); len(got) != 1 || got[0].ID != pending.CheckpointID {
		t.Fatalf("pending checkpoints after refused requests: %+v", got)
	}
	resp := request(http.MethodPost, session.ID+resolve, `{"kind":"tool_approval","action":"reject","guidance":"Keep the current state."}`)
	var resolved wire.CheckpointResponse
	testutil.FailErr(t, "decode settlement", json.Unmarshal(resp.Body.Bytes(), &resolved))
	if resp.Code != http.StatusOK || resolved.SessionID != session.ID || resolved.ID != pending.CheckpointID || resolved.Result["approved"] != false || resolved.Result["guidance"] != "Keep the current state." {
		t.Fatalf("settlement: %d %s", resp.Code, resp.Body.String())
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("pending checkpoints after settlement: %+v", got)
	}
}
