package contractfixture

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/project"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func DecodeSourceDocument(t *testing.T, w *httptest.ResponseRecorder) wire.EditorDocument {
	t.Helper()
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("document status=%d body=%s", w.Code, w.Body.String())
	}
	var doc wire.EditorDocument
	testutil.FailErr(t, "decode document", json.Unmarshal(w.Body.Bytes(), &doc))
	return doc
}

func NewSourceContentFixture(t *testing.T, raw []byte) SourceContentFixture {
	t.Helper()
	ledger, database, withLedger := TestSourceLedger(t)
	srv := NewTestServer(t, withLedger, WithSessionStore(sessionstore.NewSQL(database)), func(d *hostapi.Dependencies) {
		d.Source.EditorDocuments = editordoc.New(editordoc.NewStore(database), ledger, ledger.History, d.Core.Projects)
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "content.txt")
	testutil.FailErr(t, "write source", os.WriteFile(path, raw, 0o600))
	p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, dir)
	testutil.FailErr(t, "create project", err)
	testdbseed.InsertProjectRootWithID(t, database, p.ID, p.Roots[0].ID, dir)
	StopBackgroundOnCleanup(t, srv)
	return SourceContentFixture{Server: srv, Project: p, Path: path, Url: "/v1/projects/" + p.ID}
}

type SourceContentFixture struct {
	Server  *hostapi.Server
	Project *project.Project
	Path    string
	Url     string
}

func (f SourceContentFixture) Open(t *testing.T) wire.EditorDocument {
	t.Helper()
	w := SourceJSONRequest(t, f.Server, http.MethodPost, f.Url+"/editor-documents", wire.OpenEditorDocumentRequest{
		Path: "content.txt", RootID: f.Project.Roots[0].ID, ClientID: "window",
	})
	return DecodeSourceDocument(t, w)
}

// The wire document carries its text inside the CRDT state; the service snapshot reads it back as a string.

func (f SourceContentFixture) Text(t *testing.T, doc wire.EditorDocument) string {
	t.Helper()
	current, err := f.Server.Sources.Editor.EditorDocuments.CurrentSnapshot(t.Context(), f.Project.ID, doc.ID)
	testutil.FailErr(t, "load current document", err)
	return current.Draft
}

func SourceJSONRequest(t *testing.T, srv *hostapi.Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	testutil.FailErr(t, "encode source request", err)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, NewAuthedRequest(method, path, bytes.NewReader(data)))
	return w
}
