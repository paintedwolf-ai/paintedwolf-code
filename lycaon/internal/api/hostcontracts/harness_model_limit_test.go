package hostcontracts

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestHarnessModelLimitAuthenticatesAndPreservesAllowance(t *testing.T) {
	t.Setenv(configdir.EnvDev, "1")
	t.Setenv(configdir.EnvHarness, "1")
	sessions := store.NewMemory()
	sess, err := sessions.Create(t.Context(), wire.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create session", err)
	server := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: sessions}}), nil, hostapi.TestAPIToken)
	for _, tc := range []struct {
		name          string
		body          string
		authenticated bool
		status        int
	}{
		{"unauthenticated", fmt.Sprintf(`{"session_id":%q,"limit":2}`, sess.ID), false, http.StatusUnauthorized},
		{"unknown session", `{"session_id":"missing","limit":2}`, true, http.StatusNotFound},
		{"nonpositive limit", fmt.Sprintf(`{"session_id":%q,"limit":0}`, sess.ID), true, http.StatusBadRequest},
		{"unknown field", fmt.Sprintf(`{"session_id":%q,"limit":2,"extra":true}`, sess.ID), true, http.StatusBadRequest},
		{"install", fmt.Sprintf(`{"session_id":%q,"limit":2}`, sess.ID), true, http.StatusOK},
		{"repeat", fmt.Sprintf(`{"session_id":%q,"limit":2}`, sess.ID), true, http.StatusOK},
		{"changed allowance", fmt.Sprintf(`{"session_id":%q,"limit":3}`, sess.ID), true, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/harness/model-limit", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			if tc.authenticated {
				request.Header.Set("Authorization", "Bearer "+hostapi.TestAPIToken)
			}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, tc.status, response.Body.String())
			}
		})
	}
	testutil.FailErr(t, "verify original allowance", sessions.ConfigureModelLimit(t.Context(), sess.ID, 2))
}
