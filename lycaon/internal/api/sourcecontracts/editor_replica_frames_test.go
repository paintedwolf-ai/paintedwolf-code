package sourcecontracts

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestReplicaSynchronizationOmitsKnownTextAndKeepsUnknownBase(t *testing.T) {
	f := contractfixture.NewSecretSpanFixture(t)
	incarnation := uuid.NewString()
	for _, knownBase := range []bool{true, false} {
		request := wire.SyncEditorDocumentRequest{ClientID: "test-client", Incarnation: incarnation,
			Epoch: f.FixtureDocument.Epoch, StateVector: f.FixtureDocument.StateVector}
		if knownBase {
			request.BaseSHA256 = f.FixtureDocument.BaseSHA256
		}
		body, err := json.Marshal(request)
		testutil.FailErr(t, "encode synchronization", err)
		response := httptest.NewRecorder()
		f.Srv.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodPost,
			"/v1/projects/"+f.Project.ID+"/editor-documents/"+f.FixtureDocument.ID+"/sync", bytes.NewReader(body)))
		if response.Code != http.StatusOK {
			t.Fatalf("sync: %d %s", response.Code, response.Body.String())
		}
		var fields map[string]json.RawMessage
		testutil.FailErr(t, "decode frame", json.Unmarshal(response.Body.Bytes(), &fields))
		if _, full := fields["draft"]; full {
			t.Fatal("synchronization resent the full draft")
		}
		_, base := fields["base_content"]
		if base == knownBase {
			t.Fatalf("base presence = %v, known = %v", base, knownBase)
		}
		var frame wire.EditorReplicaFrame
		testutil.FailErr(t, "decode differential state", json.Unmarshal(response.Body.Bytes(), &frame))
		if len(frame.CRDTUpdate) > 2 || frame.Revision != f.FixtureDocument.Revision {
			t.Fatalf("already-known CRDT was resent: %+v", frame)
		}
	}
}
