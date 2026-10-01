package toolusage

import (
	"encoding/json"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPendingCheckpointsRetainApplicationScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions/root/checkpoints" || r.URL.Query().Get("include_children") != "true" || r.URL.Query().Get("status") != "pending" {
			t.Errorf("unexpected scope request: %s", r.URL)
		}
		_ = json.NewEncoder(w).Encode(wire.CheckpointListResponse{Checkpoints: []wire.CheckpointEvent{{SessionID: "child", ID: "ask"}}})
	}))
	defer server.Close()
	client := liveClient{base: server.URL, http: server.Client()}
	pending, err := client.pendingCheckpoints(t.Context(), "root")
	testutil.FailErr(t, "read scoped checkpoints", err)
	if len(pending) != 1 || pending[0].SessionID != "child" || pending[0].ID != "ask" {
		t.Fatalf("lost checkpoint owner: %+v", pending)
	}
}
