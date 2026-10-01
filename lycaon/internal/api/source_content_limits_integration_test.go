//go:build integration

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/project"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type sourceContentFixture struct {
	server  *Server
	project *project.Project
	path    string
	url     string
}

func newSourceContentFixture(t *testing.T, raw []byte) sourceContentFixture {
	t.Helper()
	ledger, database, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger, withSessionStore(sessionstore.NewSQL(database)), func(d *Dependencies) {
		d.EditorDocuments = editordoc.New(editordoc.NewStore(database), ledger, d.Projects)
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "content.txt")
	testutil.FailErr(t, "write source", os.WriteFile(path, raw, 0o600))
	p, err := project.CreateWithRoot(t.Context(), srv.projectRegistry, dir)
	testutil.FailErr(t, "create project", err)
	testdbseed.InsertProjectRootWithID(t, database, p.ID, p.Roots[0].ID, dir)
	stopBackgroundOnCleanup(t, srv)
	return sourceContentFixture{server: srv, project: p, path: path, url: "/v1/projects/" + p.ID}
}

func sourceJSONRequest(t *testing.T, srv *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	testutil.FailErr(t, "encode source request", err)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, newAuthedRequest(method, path, bytes.NewReader(data)))
	return w
}

func (f sourceContentFixture) open(t *testing.T) wire.EditorDocument {
	t.Helper()
	w := sourceJSONRequest(t, f.server, http.MethodPost, f.url+"/editor-documents", wire.OpenEditorDocumentRequest{
		Path: "content.txt", RootID: f.project.Roots[0].ID, ClientID: "window",
	})
	return decodeSourceDocument(t, w)
}

// The wire document carries its text inside the CRDT state; the service snapshot reads it back as a string.
func (f sourceContentFixture) text(t *testing.T, doc wire.EditorDocument) string {
	t.Helper()
	current, err := f.server.Sources.EditorDocuments.CurrentSnapshot(t.Context(), f.project.ID, doc.ID)
	testutil.FailErr(t, "load current document", err)
	return current.Draft
}

func decodeSourceDocument(t *testing.T, w *httptest.ResponseRecorder) wire.EditorDocument {
	t.Helper()
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("document status=%d body=%s", w.Code, w.Body.String())
	}
	var doc wire.EditorDocument
	testutil.FailErr(t, "decode document", json.Unmarshal(w.Body.Bytes(), &doc))
	return doc
}

func TestSourceContentRoundTripAtSupportedLimits(t *testing.T) {
	cases := []struct{ name, text, encoding, eol string }{
		{"above ordinary JSON limit", strings.Repeat("a", 2<<20), textfile.UTF8, "lf"},
		{"exact raw limit", strings.Repeat("a", project.SourceReadMaxBytes), textfile.UTF8, "lf"},
		{"JSON escaping", strings.Repeat("<", project.SourceReadMaxBytes), textfile.UTF8, "lf"},
		{"UTF-16 expansion", strings.Repeat("界", (project.SourceReadMaxBytes-2)/2), textfile.UTF16LEBOM, "lf"},
		{"CRLF expansion", strings.Repeat(strings.Repeat("x", 1022)+"\n", project.SourceReadMaxBytes/1024), textfile.UTF8, "crlf"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			serialized := tc.text
			if tc.eol == "crlf" {
				serialized = strings.ReplaceAll(serialized, "\n", "\r\n")
			}
			raw, err := textfile.EncodeBounded(serialized, tc.encoding, textfile.LimitsForRaw(project.SourceWriteMaxBytes))
			testutil.FailErr(t, "encode fixture", err)
			f := newSourceContentFixture(t, raw)
			doc := f.open(t)
			// A same-size edit exercises the exact raw cap, including BOM bytes.
			_, firstSize := utf8.DecodeRuneInString(tc.text)
			next := "b" + tc.text[firstSize:]
			w := sourceJSONRequest(t, f.server, http.MethodPut, f.url+"/editor-documents/"+doc.ID, wire.ReplaceEditorDocumentRequest{
				ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision, Content: next, EOL: tc.eol,
			})
			draft := decodeSourceDocument(t, w)
			saved := decodeSourceDocument(t, sourceJSONRequest(t, f.server, http.MethodPost, f.url+"/editor-documents/"+doc.ID+"/save", wire.SaveEditorDocumentRequest{
				ClientID: "window", ExpectedRevision: draft.Revision, OperationID: uuid.NewString(),
			}))
			if saved.Dirty {
				t.Fatal("saved document is dirty")
			}
			wantText := next
			if tc.eol == "crlf" {
				wantText = strings.ReplaceAll(wantText, "\n", "\r\n")
			}
			want, err := textfile.EncodeBounded(wantText, tc.encoding, textfile.LimitsForRaw(project.SourceWriteMaxBytes))
			testutil.FailErr(t, "encode expected bytes", err)
			got, err := os.ReadFile(f.path)
			testutil.FailErr(t, "read saved bytes", err)
			if !bytes.Equal(got, want) {
				t.Fatal("saved bytes differ from encoded draft")
			}
			reopened := f.open(t)
			if f.text(t, reopened) != next {
				t.Fatal("reopened document differs from saved draft")
			}
			// The direct full-replacement endpoint accepts the same supported content.
			w = sourceJSONRequest(t, f.server, http.MethodPut, f.url+"/source", wire.PutProjectSourceRequest{
				OperationID: uuid.NewString(), Path: "content.txt", RootID: f.project.Roots[0].ID,
				Content: serialized, Encoding: wire.SourceEncoding(tc.encoding), BaseSHA256: textfile.SHA256(got),
			})
			if w.Code != http.StatusOK {
				t.Fatalf("source write status=%d body=%s", w.Code, w.Body.String())
			}
			got, err = os.ReadFile(f.path)
			testutil.FailErr(t, "read replacement bytes", err)
			if !bytes.Equal(got, raw) {
				t.Fatal("direct replacement changed encoding or content")
			}
		})
	}
}

func TestEditorDraftRejectsUnsavableContentWithoutChangingRevision(t *testing.T) {
	f := newSourceContentFixture(t, []byte("base\n"))
	doc := f.open(t)
	for _, tc := range []struct {
		name, content, eol string
		status             int
		code               string
	}{
		{"raw overflow", strings.Repeat("a", project.SourceWriteMaxBytes+1), "lf", http.StatusRequestEntityTooLarge, "source_content_too_large"},
		{"decoded overflow", strings.Repeat("a", 2*project.SourceWriteMaxBytes+1), "lf", http.StatusRequestEntityTooLarge, "source_content_too_large"},
		{"EOL overflow", strings.Repeat("\n", project.SourceWriteMaxBytes/2+1), "crlf", http.StatusRequestEntityTooLarge, "source_content_too_large"},
		{"binary", "a\x00b", "lf", http.StatusBadRequest, "invalid_request"},
		{"invalid line ending", "base\n", "unknown", http.StatusBadRequest, "invalid_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := sourceJSONRequest(t, f.server, http.MethodPut, f.url+"/editor-documents/"+doc.ID, wire.ReplaceEditorDocumentRequest{
				ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision, Content: tc.content, EOL: tc.eol,
			})
			assertErrorResponse(t, w, tc.status, tc.code)
			after := f.open(t)
			if after.Revision != doc.Revision || f.text(t, after) != f.text(t, doc) || after.Dirty {
				t.Fatal("rejected update changed the durable draft")
			}
		})
	}
}

func TestEditorOpenRejectsOversizeFile(t *testing.T) {
	f := newSourceContentFixture(t, []byte(strings.Repeat("a", project.SourceReadMaxBytes+1)))
	w := sourceJSONRequest(t, f.server, http.MethodPost, f.url+"/editor-documents", wire.OpenEditorDocumentRequest{
		Path: "content.txt", RootID: f.project.Roots[0].ID, ClientID: "window",
	})
	assertErrorResponse(t, w, http.StatusRequestEntityTooLarge, "source_content_too_large")
}
