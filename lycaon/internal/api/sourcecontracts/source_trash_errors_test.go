package sourcecontracts

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSourceTrashErrorsCarryAccurateRecovery(t *testing.T) {
	s := contractfixture.NewTestServer(t)
	for _, tc := range []struct {
		err   error
		code  string
		retry bool
	}{
		{project.ErrSourcePathDenied, "source_path_denied", false},
		{project.ErrSourcePathProtected, "source_path_protected", false},
		{&project.SourceTrashFailedError{Cause: errors.New("Disk is full")}, "source_trash_failed", true},
	} {
		t.Run(tc.code, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.Sources.Workspace.WriteProjectSourceError(rec, httptest.NewRequest(http.MethodDelete, "/v1/projects", nil), tc.err)
			var body wire.ErrorResponse
			testutil.FailErr(t, "decode error", json.Unmarshal(rec.Body.Bytes(), &body))
			if string(body.Code) != tc.code || body.Retryable != tc.retry {
				t.Fatalf("recovery = %+v", body)
			}
			if strings.Contains(body.Message, "cited") || strings.Contains(body.Message, "can't open") {
				t.Fatalf("citation error leaked into deletion: %s", body.Message)
			}
			if strings.Contains(rec.Body.String(), "Disk is full") {
				t.Fatalf("OS error text reached the client: %s", rec.Body.String())
			}
		})
	}
}
