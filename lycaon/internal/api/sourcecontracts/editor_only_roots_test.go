package sourcecontracts

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/project"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/rootfacts"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// INV-5: every editor file operation works in an editor-only root of each
// class, through the same source API the editor uses everywhere.
func TestEditorOperationsWorkInEditorOnlyRoots(t *testing.T) {
	facts := rootfacts.Install(t)
	ledger, database, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, withLedger, contractfixture.WithSessionStore(sessionstore.NewSQL(database)), func(d *hostapi.Dependencies) {
		d.Source.EditorDocuments = editordoc.New(editordoc.NewStore(database), ledger, ledger.History, d.Core.Projects)
	})
	for _, class := range facts.EditorOnly() {
		t.Run(class.Name, func(t *testing.T) {
			dir := filepath.Join(class.Path, "edit-"+uuid.NewString()[:8])
			testutil.FailErr(t, "create folder", os.MkdirAll(dir, 0o700))
			testutil.FailErr(t, "seed file", os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed"), 0o600))
			p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, class.Path)
			testutil.FailErr(t, "create project", err)
			if admitted, _ := p.Roots[0].Admission(); admitted {
				t.Fatalf("%s is admitted; the case needs an editor-only root", class.Path)
			}
			root := p.Roots[0].ID
			testdbseed.InsertProjectRootWithID(t, database, p.ID, root, class.Path)
			rel := filepath.Base(dir)
			do := editorClient{t: t, srv: srv, base: "/v1/projects/" + p.ID + "/source"}

			if browse := do.call(http.MethodGet, "/browse?"+url.Values{"root_id": {root}, "path": {rel}}.Encode(), nil); !bytes.Contains(browse, []byte("seed.txt")) {
				t.Fatalf("browse did not list the folder: %s", browse)
			}
			var read wire.ProjectSourceReadResponse
			testutil.FailErr(t, "decode read", json.Unmarshal(do.call(http.MethodGet, "?"+url.Values{"root_id": {root}, "path": {rel + "/seed.txt"}}.Encode(), nil), &read))
			if read.Content != "seed" {
				t.Fatalf("read = %q", read.Content)
			}
			do.call(http.MethodPut, "", wire.PutProjectSourceRequest{OperationID: uuid.NewString(), RootID: root, Path: rel + "/seed.txt", Content: "edited", Encoding: wire.SourceEncodingUTF8, BaseSHA256: read.SHA256})
			do.call(http.MethodPost, "", wire.CreateProjectSourceEntryRequest{OperationID: uuid.NewString(), RootID: root, Path: rel + "/new.txt", Kind: "file"})
			do.call(http.MethodPost, "/rename", wire.RenameProjectSourceRequest{OperationID: uuid.NewString(), RootID: root, From: rel + "/seed.txt", To: rel + "/moved.txt"})
			do.call(http.MethodPost, "/copy", wire.CopyProjectSourceRequest{OperationID: uuid.NewString(), RootID: root, From: rel + "/moved.txt", To: rel + "/copy.txt"})
			deleted := uuid.NewString()
			do.call(http.MethodDelete, "?"+url.Values{"root_id": {root}, "path": {rel + "/copy.txt"}, "operation_id": {deleted}}.Encode(), nil)
			wantFiles(t, dir, map[string]string{"moved.txt": "edited", "new.txt": ""}, "copy.txt", "seed.txt")

			var history wire.SourceHistoryState
			testutil.FailErr(t, "decode history", json.Unmarshal(do.call(http.MethodGet, "/history", nil), &history))
			if history.Undo == nil {
				t.Fatal("history offers no undo after editor operations")
			}
			undone := history.Undo.ID
			do.call(http.MethodPost, "/history/undo", wire.SourceHistoryMutationRequest{OperationID: uuid.NewString(), ExpectedEntryID: undone})
			wantFiles(t, dir, map[string]string{"moved.txt": "edited", "copy.txt": "edited"})
			do.call(http.MethodPost, "/history/redo", wire.SourceHistoryMutationRequest{OperationID: uuid.NewString(), ExpectedEntryID: undone})
			wantFiles(t, dir, map[string]string{"moved.txt": "edited"}, "copy.txt")
		})
	}
}

type editorClient struct {
	t    *testing.T
	srv  *hostapi.Server
	base string
}

func (c editorClient) call(method, suffix string, body any) []byte {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		testutil.FailErr(c.t, "encode request", err)
		reader = bytes.NewReader(encoded)
	}
	request := contractfixture.NewAuthedRequest(method, c.base+suffix, reader)
	response := httptest.NewRecorder()
	c.srv.ServeHTTP(response, request)
	if response.Code >= http.StatusMultipleChoices {
		c.t.Fatalf("%s %s: status %d body %s", method, suffix, response.Code, response.Body.String())
	}
	return response.Body.Bytes()
}

func wantFiles(t *testing.T, dir string, present map[string]string, absent ...string) {
	t.Helper()
	for name, content := range present {
		got, err := os.ReadFile(filepath.Join(dir, name))
		testutil.FailErr(t, "read "+name, err)
		if string(got) != content {
			t.Fatalf("%s = %q, want %q", name, got, content)
		}
	}
	for _, name := range absent {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s still exists (err %v)", name, err)
		}
	}
}
