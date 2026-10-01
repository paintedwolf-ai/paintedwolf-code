package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSourceWorkspaceNamesBranchPerRoot(t *testing.T) {
	branch := sourcebranch.ForWorktree("checkout")
	p := &project.Project{ID: "project", SourceBranch: branch,
		Roots:        []project.Root{{ID: "substituted", Path: t.TempDir()}, {ID: "shared", Path: t.TempDir()}},
		RootBranches: map[string]sourcebranch.ID{"substituted": branch, "shared": sourcebranch.Trunk}}
	result := sourceapi.SourceWorkspaceDTO(p, false, wire.SourceInventoryState{})
	if result.Roots[0].BranchID != string(branch) || result.Roots[1].BranchID != "" {
		t.Fatalf("workspace changed an unaffected root's identity: %+v", result.Roots)
	}
}

func TestWorkerSourceReadsUseRecordedRootLayout(t *testing.T) {
	queue := worker.NewInMemoryQueue(1)
	srv := newTestServer(t, withWorkers(queue), withSQLProjects(t))
	primary, branchRoot := t.TempDir(), t.TempDir()
	p := createProjectForTest(t, srv, primary)
	rootID := p.Roots[0].ID
	roots := []workspace.JobMetaRoot{
		{ID: rootID, Path: primary, Label: "previous label", IsPrimary: true},
		{ID: "detached-root", Path: t.TempDir(), Label: "detached"},
	}
	for _, root := range roots {
		dir, err := projectroot.BranchDirForID(root.ID)
		testutil.FailErr(t, "branch directory", err)
		abs := filepath.Join(branchRoot, dir)
		testutil.FailErr(t, "make recorded root", os.MkdirAll(abs, 0700))
		testutil.FailErr(t, "write worker contents", os.WriteFile(filepath.Join(abs, "same.go"), []byte("worker\n"), 0600))
	}
	testutil.FailErr(t, "write primary contents", os.WriteFile(filepath.Join(primary, "same.go"), []byte("primary\n"), 0600))
	testutil.FailErr(t, "record worker roots", workspace.WriteJobMeta(enginepaths.MetaDirForBranchRoot(branchRoot), workspace.JobMeta{
		Roots: roots, SnapshotComplete: true,
	}))
	workerID := enqueueBranchJob(t, queue, p.ID, branchRoot)
	read := func(root string) *httptest.ResponseRecorder {
		t.Helper()
		q := url.Values{"path": {"same.go"}, "root_id": {root}, "worker_id": {workerID}, "include_deleted": {"true"}}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, newAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source?"+q.Encode(), nil))
		return rec
	}
	rec := read(rootID)
	if rec.Code != http.StatusOK {
		t.Fatalf("worker read = %d %s", rec.Code, rec.Body.String())
	}
	var result wire.ProjectSourceReadResponse
	testutil.FailErr(t, "decode worker contents", json.Unmarshal(rec.Body.Bytes(), &result))
	if result.Content != "worker\n" || result.WorkspaceKind != wire.SourceWorkspaceKindWorker {
		t.Fatalf("worker source = %+v", result)
	}
	if rec := read("detached-root"); rec.Code != http.StatusNotFound {
		t.Fatalf("detached read = %d %s", rec.Code, rec.Body.String())
	}
	dir, err := projectroot.BranchDirForID(rootID)
	testutil.FailErr(t, "worker root", err)
	testutil.FailErr(t, "remove worker file", os.Remove(filepath.Join(branchRoot, dir, "same.go")))
	if rec := read(rootID); rec.Code != http.StatusNotFound {
		t.Fatalf("missing worker path = %d %s", rec.Code, rec.Body.String())
	}
}
