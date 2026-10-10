package sourcecontracts

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestOpenEditorDocumentRetainedReplicaProjection(t *testing.T) {
	f := contractfixture.NewSecretSpanFixture(t)
	Open := func(replica *wire.RetainedReplica) wire.EditorDocument {
		body, err := json.Marshal(wire.OpenEditorDocumentRequest{Path: f.FixtureDocument.Path, RootID: f.FixtureDocument.RootID, ClientID: "test-client", Replica: replica})
		testutil.FailErr(t, "encode open", err)
		response := httptest.NewRecorder()
		f.Srv.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodPost, "/v1/projects/"+f.Project.ID+"/editor-documents", bytes.NewReader(body)))
		if response.Code != http.StatusCreated {
			t.Fatalf("open: %d %s", response.Code, response.Body.String())
		}
		var opened wire.EditorDocument
		testutil.FailErr(t, "decode open", json.Unmarshal(response.Body.Bytes(), &opened))
		return opened
	}
	full := Open(nil)
	replaceBody, err := json.Marshal(wire.ReplaceEditorDocumentRequest{ClientID: "test-client", OperationID: uuid.NewString(), ExpectedRevision: full.Revision, Content: contractfixture.FileWithSecret + "// dirty\n", EOL: "lf"})
	testutil.FailErr(t, "encode replace", err)
	replaced := httptest.NewRecorder()
	f.Srv.ServeHTTP(replaced, contractfixture.NewAuthedRequest(http.MethodPut, "/v1/projects/"+f.Project.ID+"/editor-documents/"+full.ID, bytes.NewReader(replaceBody)))
	if replaced.Code != http.StatusOK {
		t.Fatalf("replace: %d %s", replaced.Code, replaced.Body.String())
	}
	dirty := Open(nil)
	if dirty.BaseContent == nil {
		t.Fatal("a dirty document without a retained replica must carry its saved base")
	}
	current := &wire.RetainedReplica{DocumentID: dirty.ID, Epoch: dirty.Epoch, StateVector: dirty.StateVector, BaseSHA256: dirty.BaseSHA256}
	differential := Open(current)
	if len(differential.CRDTUpdate) >= len(dirty.CRDTUpdate) || differential.BaseContent != nil {
		t.Fatalf("retained identity must narrow the projection and omit the known base: %d bytes vs %d, base=%v", len(differential.CRDTUpdate), len(dirty.CRDTUpdate), differential.BaseContent != nil)
	}
	foreign := Open(&wire.RetainedReplica{DocumentID: "00000000-0000-0000-0000-000000000000", Epoch: dirty.Epoch, StateVector: dirty.StateVector, BaseSHA256: dirty.BaseSHA256})
	if len(foreign.CRDTUpdate) != len(dirty.CRDTUpdate) || foreign.BaseContent == nil {
		t.Fatalf("a foreign identity must receive the full snapshot with its saved base: %d bytes vs %d, base=%v", len(foreign.CRDTUpdate), len(dirty.CRDTUpdate), foreign.BaseContent != nil)
	}
}

func TestOpenEditorDocumentBinaryAndPathConfinement(t *testing.T) {
	f := contractfixture.NewSecretSpanFixture(t)
	testutil.FailErr(t, "write binary", os.WriteFile(filepath.Join(f.Project.Roots[0].Path, "data.bin"), []byte{0, 1, 2, 3}, 0o644))
	for _, path := range []string{"data.bin", "../outside.txt"} {
		body, err := json.Marshal(wire.OpenEditorDocumentRequest{Path: path, RootID: f.Project.Roots[0].ID, ClientID: "test-client"})
		testutil.FailErr(t, "encode open", err)
		response := httptest.NewRecorder()
		f.Srv.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodPost, "/v1/projects/"+f.Project.ID+"/editor-documents", bytes.NewReader(body)))
		if response.Code < 400 || response.Code >= 500 {
			t.Fatalf("expected error for %q: %d %s", path, response.Code, response.Body.String())
		}
	}
}
