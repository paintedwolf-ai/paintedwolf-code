package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type fixedInvocationRecorder struct {
	items []wire.InvocationReceipt
}

func (*fixedInvocationRecorder) Begin(context.Context, invocation.Start) (*wire.InvocationReceipt, error) {
	return nil, nil
}

func (*fixedInvocationRecorder) Settle(context.Context, string, invocation.Settlement) (*wire.InvocationReceipt, error) {
	return nil, nil
}

func (r *fixedInvocationRecorder) ListSession(context.Context, string) ([]wire.InvocationReceipt, error) {
	return r.items, nil
}

func (r *fixedInvocationRecorder) ListSessionPage(ctx context.Context, sessionID string, cursor string, limit int) (wire.InvocationReceiptList, error) {
	return wire.InvocationReceiptList{Invocations: r.items}, nil
}

func (*fixedInvocationRecorder) InterruptRunning(context.Context) (int64, error) {
	return 0, nil
}

func TestListSessionInvocations(t *testing.T) {
	started := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	recorder := &fixedInvocationRecorder{items: []wire.InvocationReceipt{{
		ID: "inv-1", Tool: "read", ToolCallID: "call-1", Owner: "filesystem",
		Lifecycle: "read_only", Status: wire.InvocationStatusCompleted,
		Evidence: wire.InvocationEvidence{Kind: "result", Ref: "message-1"}, StartedAt: started,
	}}}
	srv := newTestServer(t, func(d *Dependencies) { d.Invocations = recorder })
	sess, err := srv.sessionStore.Create(t.Context(), wire.CreateSessionRequest{
		Posture: wire.SessionPostureBuild,
	}, "project-1")
	testutil.FailErr(t, "create session", err)

	req := newAuthedRequest(http.MethodGet, "/v1/sessions/"+sess.ID+"/invocations", nil)
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
