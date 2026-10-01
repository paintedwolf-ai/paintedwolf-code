package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestReplicaSynchronizationOmitsKnownTextAndKeepsUnknownBase(t *testing.T) {
	f := newSecretSpanFixture(t)
	incarnation := uuid.NewString()
	for _, knownBase := range []bool{true, false} {
		request := wire.SyncEditorDocumentRequest{ClientID: "test-client", Incarnation: incarnation,
			Epoch: f.document.Epoch, StateVector: f.document.StateVector}
		if knownBase {
			request.BaseSHA256 = f.document.BaseSHA256
		}
		body, err := json.Marshal(request)
		testutil.FailErr(t, "encode synchronization", err)
		response := httptest.NewRecorder()
		f.srv.ServeHTTP(response, newAuthedRequest(http.MethodPost,
			"/v1/projects/"+f.project.ID+"/editor-documents/"+f.document.ID+"/sync", bytes.NewReader(body)))
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
		if len(frame.CRDTUpdate) > 2 || frame.Revision != f.document.Revision {
			t.Fatalf("already-known CRDT was resent: %+v", frame)
		}
	}
}
