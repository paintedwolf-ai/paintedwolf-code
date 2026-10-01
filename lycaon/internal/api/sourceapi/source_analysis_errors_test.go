package sourceapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/repomap"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSourceAnalysisFailuresRemainDistinctFromServerFailures(t *testing.T) {
	server := newSourceHandlerFixture(t)
	for _, item := range []struct {
		err  error
		code wire.ApiErrorCode
	}{
		{repomap.ErrDefinitionIncomplete, wire.ApiErrorCodeSourceAnalysisIncomplete},
		{repomap.ErrDefinitionUnavailable, wire.ApiErrorCodeSourceAnalysisUnavailable},
	} {
		response := httptest.NewRecorder()
		server.writeSourceAnalysisError(response, httptest.NewRequest(http.MethodGet, "/v1/projects", nil), item.err)
		if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "30" {
			t.Fatalf("analysis failure: status=%d headers=%v", response.Code, response.Header())
		}
		var body wire.ErrorResponse
		testutil.FailErr(t, "decode analysis error", json.Unmarshal(response.Body.Bytes(), &body))
		if body.Code != item.code {
			t.Fatalf("code = %s, want %s", body.Code, item.code)
		}
	}
}
