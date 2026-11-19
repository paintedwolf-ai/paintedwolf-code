//go:build darwin || linux

package editoradmin

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestOpenEditorDocumentResolvesAHeldOutsideFile(t *testing.T) {
	f := newEditorFixture(t)
	folder, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve outside fixture", err)
	name := filepath.Join(folder, "notes.txt")
	content := []byte("outside notes\n")
	testutil.FailErr(t, "write outside notes", os.WriteFile(name, content, 0o600))
	rel, ok := projectroot.OutsideRel(name)
	if !ok {
		t.Fatal("fixture lacks an outside address")
	}
	project, err := f.h.ProjectRegistry.Get(t.Context(), f.project.ID)
	testutil.FailErr(t, "read project namespace", err)
	outside := project.Outside.ID
	open := func(request wire.OpenEditorDocumentRequest) *httptest.ResponseRecorder {
		request.ClientID = "window"
		return f.serve(t, http.MethodPost, f.projectPath(""), request)
	}
	expectCode(t, "unheld outside path", open(wire.OpenEditorDocumentRequest{RootID: outside, Path: rel}), wire.ApiErrorCodeSourcePathDenied)
	expectCode(t, "unknown file identity", open(wire.OpenEditorDocumentRequest{FileID: uuid.NewString()}), wire.ApiErrorCodeEditorDocumentNotFound)

	const chatID = "outside-chat"
	testdbseed.InsertSession(t, f.h.SourceLedger.LedgerDB(), chatID, project.ID)
	file, err := f.h.SourceLedger.TrackChatFile(t.Context(), sourceledger.TrackInput{
		ProjectID: project.ID, RootID: outside, Path: rel, Content: content,
	}, chatID, sourceledger.ChatFileOpened)
	testutil.FailErr(t, "track opened outside file", err)
	doc := decodeStatus[wire.EditorDocument](t, "open held identity", open(wire.OpenEditorDocumentRequest{
		FileID: file.FileID, RootID: f.project.Roots[0].ID, Path: "a.txt",
	}), http.StatusCreated)
	if doc.FileID != file.FileID || doc.RootID != outside || doc.Path != rel || doc.BaseSHA256 != fmt.Sprintf("%x", sha256.Sum256(content)) {
		t.Fatalf("editor opened an unexpected file: %+v", doc)
	}
}
