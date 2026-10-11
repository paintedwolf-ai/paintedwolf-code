package sessioncontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestListSessionInvocations(t *testing.T) {
	started := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	recorder := &contractfixture.FixedInvocationRecorder{Items: []wire.InvocationReceipt{{
		ID: "inv-1", Tool: "read", ToolCallID: "call-1", Owner: "filesystem",
		Lifecycle: "read_only", Status: wire.InvocationStatusCompleted,
		Evidence: wire.InvocationEvidence{Kind: "result", Ref: "message-1"}, StartedAt: started,
	}}}
	srv := contractfixture.NewTestServer(t, func(d *hostapi.Dependencies) { d.Core.Invocations = recorder })
	sess, err := srv.Sources.Workspace.SessionStore.Create(t.Context(), wire.CreateSessionRequest{
		Posture: wire.SessionPostureBuild,
	}, "project-1")
	testutil.FailErr(t, "create session", err)

	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/sessions/"+sess.ID+"/invocations", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var body wire.InvocationReceiptList
	testutil.FailErr(t, "decode response", json.Unmarshal(rec.Body.Bytes(), &body))
	if len(body.Invocations) != 1 || body.Invocations[0].ID != "inv-1" || body.Invocations[0].Evidence.Ref != "message-1" {
		t.Fatalf("body = %+v", body)
	}
}
