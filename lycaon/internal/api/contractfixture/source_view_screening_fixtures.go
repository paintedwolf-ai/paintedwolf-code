package contractfixture

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func AwaitComparisonScreen(t *testing.T, server *hostapi.Server, projectID, viewID string) wire.SourceComparisonView {
	t.Helper()
	var view wire.SourceComparisonView
	testutil.WaitFor(t, 5*time.Second, func() bool {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, NewAuthedRequest(http.MethodGet, sourceapi.SourceViewURL(projectID, viewID), nil))
		view = *ReadSourceViewResponse(t, response, http.StatusOK).Comparison
		return view.Comparison != nil &&
			(view.Comparison.After != nil && view.Comparison.After.SecretScreen != nil ||
				view.Comparison.Before != nil && view.Comparison.Before.SecretScreen != nil)
	})
	return view
}
