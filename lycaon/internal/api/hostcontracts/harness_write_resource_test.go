package hostcontracts

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/harnessfixture"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHarnessWriteResourceAuthenticatesBoundaryRefusal(t *testing.T) {
	t.Setenv(configdir.EnvDev, "1")
	t.Setenv(configdir.EnvHarness, "1")
	server := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: sessionstore.NewMemory()}}), nil, hostapi.TestAPIToken)
	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/harness/write-resource", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "resolve home", err)
	body, err := json.Marshal(harnessfixture.WriteResourceRequest{Capture: t.TempDir(), Project: home})
	testutil.FailErr(t, "encode allocation", err)
	request := httptest.NewRequest(http.MethodPost, "/harness/write-resource", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+hostapi.TestAPIToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("allocation status = %d: %s", response.Code, response.Body.String())
	}
	var refusal struct {
		Code string `json:"code"`
	}
	testutil.FailErr(t, "decode refusal", json.Unmarshal(response.Body.Bytes(), &refusal))
	if refusal.Code != "invalid_request" {
		t.Fatalf("refusal = %+v", refusal)
	}
}
