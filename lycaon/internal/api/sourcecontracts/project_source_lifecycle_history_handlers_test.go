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
	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/project"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestProjectSourceLifecycleHistoryRoutes(t *testing.T) {
	ledger, database, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, withLedger, contractfixture.WithSessionStore(sessionstore.NewSQL(database)), func(d *hostapi.Dependencies) {
		d.Source.EditorDocuments = editordoc.New(editordoc.NewStore(database), ledger, d.Core.Projects)
	})
	root := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, root)
	testutil.FailErr(t, "create project", err)
	testdbseed.InsertProjectRootWithID(t, database, p.ID, p.Roots[0].ID, root)
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(root, "note.txt"), []byte("kept"), 0o640))

	renameID := uuid.NewString()
	renameBody, err := json.Marshal(wire.RenameProjectSourceRequest{
		OperationID: renameID, RootID: p.Roots[0].ID, From: "note.txt", To: "archive/note.txt",
	})
	testutil.FailErr(t, "encode rename", err)
	request := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/projects/"+p.ID+"/source/rename", bytes.NewReader(renameBody))
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("rename status = %d body=%s", response.Code, response.Body.String())
	}

	request = contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source/history", nil)
	response = httptest.NewRecorder()
	srv.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("history status = %d body=%s", response.Code, response.Body.String())
	}
	var history wire.SourceHistoryState
	testutil.FailErr(t, "decode history", json.Unmarshal(response.Body.Bytes(), &history))
	if history.Undo == nil || history.Undo.ID != renameID || history.Redo != nil {
		t.Fatalf("history = %+v", history)
	}

	undoBody, err := json.Marshal(wire.SourceHistoryMutationRequest{
		OperationID: uuid.NewString(), ExpectedEntryID: renameID,
	})
	testutil.FailErr(t, "encode undo", err)
	request = contractfixture.NewAuthedRequest(http.MethodPost, "/v1/projects/"+p.ID+"/source/history/undo", bytes.NewReader(undoBody))
	response = httptest.NewRecorder()
	srv.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("undo status = %d body=%s", response.Code, response.Body.String())
	}
	var mutation wire.SourceHistoryMutationResponse
	testutil.FailErr(t, "decode undo", json.Unmarshal(response.Body.Bytes(), &mutation))
	if mutation.Op != wire.SourceChangeOpRename || mutation.FromPath != "archive/note.txt" || mutation.Path != "note.txt" {
		t.Fatalf("undo mutation = %+v", mutation)
	}
	content, err := os.ReadFile(filepath.Join(root, "note.txt"))
	testutil.FailErr(t, "read restored source", err)
	if string(content) != "kept" {
		t.Fatalf("restored content = %q", content)
	}
}
