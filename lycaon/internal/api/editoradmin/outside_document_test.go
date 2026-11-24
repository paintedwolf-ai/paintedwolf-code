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
	request := wire.OpenEditorDocumentRequest{FileID: file.FileID}
	doc := decodeStatus[wire.EditorDocument](t, "open held identity", open(request), http.StatusCreated)
	if doc.FileID != file.FileID || doc.RootID != outside || doc.Path != rel || doc.BaseSHA256 != fmt.Sprintf("%x", sha256.Sum256(content)) {
		t.Fatalf("editor opened an unexpected file: %+v", doc)
	}
	_, err = f.h.SourceLedger.LedgerDB().ExecContext(t.Context(), "DELETE FROM sessions WHERE id=?", chatID)
	testutil.FailErr(t, "delete the chat", err)
	reopened := decodeStatus[wire.EditorDocument](t, "reopen by retained document", open(request), http.StatusCreated)
	if reopened.ID != doc.ID || reopened.FileID != file.FileID {
		t.Fatalf("reopened editor changed file identity: %+v", reopened)
	}
	testutil.FailErr(t, "move the held folder", os.Rename(folder, folder+"-old"))
	t.Cleanup(func() { testutil.FailErr(t, "remove the moved folder", os.RemoveAll(folder+"-old")) })
	testutil.FailErr(t, "create replacement folder", os.Mkdir(folder, 0o700))
	testutil.FailErr(t, "create replacement file", os.WriteFile(name, []byte("different file"), 0o600))
	expectCode(t, "reopen with replaced parent", open(request), wire.ApiErrorCodeSourcePathDenied)
}
