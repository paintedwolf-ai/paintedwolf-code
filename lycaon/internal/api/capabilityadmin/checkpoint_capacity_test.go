package capabilityadmin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/usernotice"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestApprovalCapacityResponsePreservesTheRecoveryAction(t *testing.T) {
	config, err := usernotice.LoadNoticeDir(filepath.Join("..", "..", "..", "config", "packs", "painted-wolf", "platform", "host", "user-notices"))
	testutil.FailErr(t, "load host notices", err)
	actions := &CheckpointActions{responses: &httpio.Responder{Notices: usernotice.NewCatalog(config)}}
	response := httptest.NewRecorder()
	limit := &toolcontract.ProgramMemoryLimitError{Account: "state", Maximum: 64 << 20, Observed: (64 << 20) + 1}
	actions.writeCheckpointResolveError(response, httptest.NewRequest(http.MethodPost, "/v1/sessions/session/checkpoints/checkpoint", nil), fmt.Errorf("approval authority: %w", limit))
	var body wire.ErrorResponse
	testutil.FailErr(t, "decode capacity response", json.Unmarshal(response.Body.Bytes(), &body))
	if response.Code != http.StatusUnprocessableEntity || body.Code != wire.ApiErrorCodeApprovalCapacityExceeded || body.Retryable {
		t.Fatalf("capacity refusal lost its identity: status=%d body=%+v", response.Code, body)
	}
	if !strings.Contains(body.Message, "still waiting") || !strings.Contains(body.SuggestedAction, "Reject") || body.Details["account"] != "state" {
		t.Fatalf("capacity refusal lost recovery or accounting: %+v", body)
	}
}
