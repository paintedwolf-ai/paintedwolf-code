package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/cost/costtest"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestPromptHTTPReturnsSpendRejectionBeforeAcceptedReceipt(t *testing.T) {
	// The session is created by an ordinary server; the spend-capped manager then
	// serves the same store and projects.
	fixture := newTestServer(t)
	sess := createSessionAtPathOnServer(t, fixture, t.TempDir(), wire.SessionPostureBuild)
	tracker := costtest.NewTracker(t, nil)
	limits := settings.DefaultSessionLimits()
	limits.SpendCeilingEnabled = true
	limits.SessionSpendCeilingUSD = 5
	srv := newTestServer(t, func(d *Dependencies) {
		d.Store, d.Projects = fixture.sessionStore, fixture.projectRegistry
		d.Sessions = session.NewManagerWithLLMService(d.Store, llm.NewMockProvider(nil), nil, tools.NewStubRegistry(), limits, tracker)
	})

	spent := int64(6_000_000_000)
	testutil.FailErr(t, "record spend", tracker.RecordUsage(t.Context(), cost.UsageEvent{
		SessionID: sess.ID, Caller: cost.CallerCoordinator, PromptTokens: 10, EstimatedNanoUSD: &spent,
	}))
	request := wire.PromptRequest{OperationID: uuid.NewString(), Text: "Keep this unsent draft"}
	body, err := json.Marshal(request)
	testutil.FailErr(t, "encode prompt", err)
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, newAuthedRequest(http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts", strings.NewReader(string(body))))
	var notice wire.ErrorResponse
	testutil.FailErr(t, "decode rejection", json.Unmarshal(response.Body.Bytes(), &notice))
	if response.Code != http.StatusConflict || notice.Code != wire.ApiErrorCodeSessionSpendCeilingReached || notice.Message == "" {
		t.Fatalf("prompt was not rejected synchronously: status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := srv.sessions.GetPromptSubmission(t.Context(), request.OperationID); !errors.Is(err, store.ErrPromptSubmissionNotFound) {
		t.Fatalf("rejected request left an accepted receipt: %v", err)
	}
}
