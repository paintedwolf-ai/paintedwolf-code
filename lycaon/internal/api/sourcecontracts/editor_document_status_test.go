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

func TestEditorDocumentMetadataAndTabRetention(t *testing.T) {
	f := contractfixture.NewSecretSpanFixture(t)
	endpoint := "/v1/projects/" + f.Project.ID + "/editor-documents"
	missing := uuid.NewString()
	body, err := json.Marshal(wire.EditorDocumentStatusesRequest{DocumentIds: []string{f.FixtureDocument.ID, missing}})
	testutil.FailErr(t, "encode metadata identities", err)
	unauthorized := httptest.NewRecorder()
	f.Srv.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, endpoint+"/status", bytes.NewReader(body)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized metadata status = %d", unauthorized.Code)
	}
	response := httptest.NewRecorder()
	f.Srv.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodPost, endpoint+"/status", bytes.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("metadata status = %d: %s", response.Code, response.Body.String())
	}
	var statuses wire.EditorDocumentStatuses
	testutil.FailErr(t, "decode document metadata", json.Unmarshal(response.Body.Bytes(), &statuses))
	if len(statuses.Documents) != 1 || statuses.Documents[0].ID != f.FixtureDocument.ID || len(statuses.Missing) != 1 || statuses.Missing[0] != missing {
		t.Fatalf("metadata identities = %+v", statuses)
	}
	for _, forbidden := range []string{`"draft":`, `"base_content":`, `"crdt_update":`} {
		if bytes.Contains(response.Body.Bytes(), []byte(forbidden)) {
			t.Fatalf("metadata admitted document bytes: %s", forbidden)
		}
	}
	for _, ids := range [][]string{{f.FixtureDocument.ID}, {}} {
		body, err := json.Marshal(wire.ReplaceEditorDocumentRetentionRequest{ClientID: "window:metadata", DocumentIds: ids})
		testutil.FailErr(t, "encode tab references", err)
		response = httptest.NewRecorder()
		f.Srv.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodPut, endpoint+"/retention", bytes.NewReader(body)))
		if response.Code != http.StatusNoContent {
			t.Fatalf("tab retention status = %d: %s", response.Code, response.Body.String())
		}
	}
}

func TestEditorDocumentMetadataRejectsUnboundedSelection(t *testing.T) {
	f := contractfixture.NewSecretSpanFixture(t)
	for _, ids := range [][]string{{}, {"not-an-identity"}, make([]string, 65)} {
		body, err := json.Marshal(wire.EditorDocumentStatusesRequest{DocumentIds: ids})
		testutil.FailErr(t, "encode invalid metadata selection", err)
		response := httptest.NewRecorder()
		f.Srv.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodPost, "/v1/projects/"+f.Project.ID+"/editor-documents/status", bytes.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid metadata selection status = %d", response.Code)
		}
	}
}
