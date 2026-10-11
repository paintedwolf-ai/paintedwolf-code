package editoradmin

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestDocumentStatusesReportPresentAndMissingIdentities(t *testing.T) {
	f := newEditorFixture(t)
	d := f.replace(t, f.open(t, "a.txt", "window"), "draft\n")
	missing := uuid.NewString()
	request := wire.EditorDocumentStatusesRequest{DocumentIds: []string{d.ID, missing}}
	statuses := decodeStatus[wire.EditorDocumentStatuses](t, "statuses", f.serve(t, http.MethodPost, f.projectPath("/status"), request), http.StatusOK)
	if len(statuses.Documents) != 1 || statuses.Documents[0].ID != d.ID || !statuses.Documents[0].Dirty {
		t.Fatalf("documents = %+v, want the dirty document", statuses.Documents)
	}
	if len(statuses.Missing) != 1 || statuses.Missing[0] != missing {
		t.Fatalf("missing = %v, want %s", statuses.Missing, missing)
	}
}

func TestDocumentStatusesRefuseUnboundedSelections(t *testing.T) {
	f := newEditorFixture(t)
	expectCode(t, "malformed", f.serve(t, http.MethodPost, f.projectPath("/status"), "{"), wire.ApiErrorCodeInvalidJson)
	for name, ids := range map[string][]string{
		"empty":    {},
		"too many": make([]string, 65),
		"invalid":  {"not-an-identity"},
	} {
		response := f.serve(t, http.MethodPost, f.projectPath("/status"), wire.EditorDocumentStatusesRequest{DocumentIds: ids})
		expectCode(t, name, response, wire.ApiErrorCodeInvalidRequest)
	}
	unknown := "/v1/projects/" + uuid.NewString() + "/editor-documents/status"
	expectCode(t, "unknown project", f.serve(t, http.MethodPost, unknown, wire.EditorDocumentStatusesRequest{DocumentIds: []string{uuid.NewString()}}), wire.ApiErrorCodeProjectNotFound)
}

func TestTabRetentionReplacesTheClientsReferences(t *testing.T) {
	f := newEditorFixture(t)
	d := f.open(t, "a.txt", "window")
	// The second inventory omits window:old, whose stream is gone, so its marks are swept.
	for _, request := range []wire.ReplaceEditorDocumentRetentionRequest{
		{ClientID: "window:old", RetainedClients: []string{"window:old"}, DocumentIds: []string{d.ID}},
		{ClientID: "window:main", RetainedClients: []string{"window:main"}, DocumentIds: []string{d.ID}},
		{ClientID: "window:main", DocumentIds: []string{}},
	} {
		if response := f.serve(t, http.MethodPut, f.projectPath("/retention"), request); response.Code != http.StatusNoContent {
			t.Fatalf("retention %+v = %d: %s", request, response.Code, response.Body.String())
		}
	}
	outside := wire.ReplaceEditorDocumentRetentionRequest{ClientID: "window:main", RetainedClients: []string{"window:other"}, DocumentIds: []string{}}
	expectCode(t, "client outside its inventory", f.serve(t, http.MethodPut, f.projectPath("/retention"), outside), wire.ApiErrorCodeEditorReplicaIdentity)
	unknown := "/v1/projects/" + uuid.NewString() + "/editor-documents/retention"
	body := `{"client_id":"window:main","retained_clients":["window:main"],"document_ids":[]}`
	expectCode(t, "unknown project", f.serve(t, http.MethodPut, unknown, body), wire.ApiErrorCodeProjectNotFound)
}

func TestTabRetentionValidatesBeforeAdmission(t *testing.T) {
	f := newEditorFixture(t)
	tooMany := make([]string, 16385)
	for i := range tooMany {
		tooMany[i] = uuid.NewString()
	}
	cases := []struct {
		name  string
		body  any
		code  wire.ApiErrorCode
		field string
	}{
		{name: "malformed", body: "{", code: wire.ApiErrorCodeInvalidJson},
		{name: "omitted keys", body: `{"client_id":"window"}`, code: wire.ApiErrorCodeInvalidRequest, field: "retained_clients"},
		{name: "blank client", body: `{"client_id":" ","retained_clients":null,"document_ids":[]}`, code: wire.ApiErrorCodeInvalidRequest, field: "client_id"},
		{name: "too many", body: wire.ReplaceEditorDocumentRetentionRequest{ClientID: "window", DocumentIds: tooMany}, code: wire.ApiErrorCodeInvalidRequest},
		{name: "invalid identity", body: wire.ReplaceEditorDocumentRetentionRequest{ClientID: "window", DocumentIds: []string{"tab"}}, code: wire.ApiErrorCodeInvalidRequest},
	}
	holdProjectMutation(t, f)
	for _, tc := range cases {
		failure := expectCode(t, tc.name, f.serve(t, http.MethodPut, f.projectPath("/retention"), tc.body), tc.code)
		if tc.field != "" && failure.Details["field"] != tc.field {
			t.Fatalf("%s refused field = %v, want %s", tc.name, failure.Details["field"], tc.field)
		}
	}
}

// holdProjectMutation keeps the project busy so validation must answer first.
func holdProjectMutation(t *testing.T, f *editorFixture) {
	t.Helper()
	testutil.FailErr(t, "begin project mutation", f.h.MutationGate.BeginMutation(f.project.ID))
	t.Cleanup(func() { f.h.MutationGate.EndMutation(f.project.ID) })
}
