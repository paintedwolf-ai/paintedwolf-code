package editoradmin

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (f *editorFixture) viewPath() string { return "/v1/projects/" + f.project.ID + "/view" }

func folderView(client string) wire.ReplaceProjectViewRequest {
	return wire.ReplaceProjectViewRequest{ClientID: client, Focused: true, Folder: &wire.ViewReportPath{RootID: uuid.NewString(), Path: "src"}, Findings: []wire.ViewReportFinding{}}
}

func TestProjectViewRecordsAndWithdrawsTheCallersWindow(t *testing.T) {
	f := newEditorFixture(t)
	if response := f.serve(t, http.MethodPut, f.viewPath(), folderView("window:main")); response.Code != http.StatusNoContent {
		t.Fatalf("report = %d: %s", response.Code, response.Body.String())
	}
	reports := f.h.ProjectViews.Visible(f.project.ID, f.owner.ID)
	if len(reports) != 1 || reports[0].ClientID != "window:main" || reports[0].View.Folder == nil || reports[0].View.Folder.Path != "src" {
		t.Fatalf("recorded reports = %+v", reports)
	}
	empty := wire.ReplaceProjectViewRequest{ClientID: "window:main", Findings: []wire.ViewReportFinding{}}
	if response := f.serve(t, http.MethodPut, f.viewPath(), empty); response.Code != http.StatusNoContent {
		t.Fatalf("withdraw = %d: %s", response.Code, response.Body.String())
	}
	if reports := f.h.ProjectViews.Visible(f.project.ID, f.owner.ID); len(reports) != 0 {
		t.Fatalf("withdrawn window still reported: %+v", reports)
	}
}

func TestProjectViewRefusesAnotherPersonsWindowAndBadReports(t *testing.T) {
	f := newEditorFixture(t)
	if err := f.h.ProjectViews.Replace(f.project.ID, uuid.NewString(), folderView("window:theirs")); err != nil {
		t.Fatalf("seed another person's window: %v", err)
	}
	expectCode(t, "another person's window", f.serve(t, http.MethodPut, f.viewPath(), folderView("window:theirs")), wire.ApiErrorCodeEditorReplicaIdentity)
	expectCode(t, "malformed", f.serve(t, http.MethodPut, f.viewPath(), "{"), wire.ApiErrorCodeInvalidJson)
	expectCode(t, "omitted surfaces", f.serve(t, http.MethodPut, f.viewPath(), `{"client_id":"window:main","focused":true}`), wire.ApiErrorCodeInvalidRequest)
	bad := folderView("window:main")
	bad.Folder.Path = "../outside"
	expectCode(t, "path outside its root", f.serve(t, http.MethodPut, f.viewPath(), bad), wire.ApiErrorCodeInvalidRequest)
	unknown := "/v1/projects/" + uuid.NewString() + "/view"
	expectCode(t, "unknown project", f.serve(t, http.MethodPut, unknown, folderView("window:main")), wire.ApiErrorCodeProjectNotFound)
}
