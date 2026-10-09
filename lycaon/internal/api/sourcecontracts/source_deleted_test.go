package sourcecontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/secretspan"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestDeletedSourceNavigationAndReadPreferCurrentPath(t *testing.T) {
	ledger, ledgerDB, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, withLedger)
	root := t.TempDir()
	p := contractfixture.CreateProjectForTest(t, srv, root)
	contractfixture.MirrorLedgerProject(t, ledgerDB, p)
	rootID := p.Roots[0].ID
	testutil.FailErr(t, "record deletion", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID, RootID: rootID, Path: "gone.go", Op: wire.SourceChangeOpDelete,
		Origin: wire.SourceChangeOriginUser, Before: []byte("retained\n"),
	}))
	project, err := srv.Sources.Workspace.ProjectRegistry.Get(t.Context(), p.ID)
	testutil.FailErr(t, "get project", err)
	refs := []wire.NavigationReference{{ID: "r", ProjectID: p.ID, RootID: rootID, Path: "gone.go", Status: wire.NavigationResolved, EntryKind: "file"}}
	resolved := srv.Sources.Workspace.ResolveNavigationPaths(t.Context(), project, sourcebranch.Trunk, refs)
	if resolved[0].Status != wire.NavigationResolved || !resolved[0].Deleted {
		t.Fatalf("deleted navigation = %+v", resolved)
	}
	get := func(include bool) (int, wire.ProjectSourceReadResponse) {
		t.Helper()
		q := url.Values{"path": {"gone.go"}, "root_id": {rootID}}
		if include {
			q.Set("include_deleted", "true")
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source?"+q.Encode(), nil))
		var result wire.ProjectSourceReadResponse
		if rec.Code == http.StatusOK {
			testutil.FailErr(t, "decode source", json.Unmarshal(rec.Body.Bytes(), &result))
		}
		return rec.Code, result
	}
	if status, _ := get(false); status != http.StatusNotFound {
		t.Fatalf("ordinary read = %d", status)
	}
	status, result := get(true)
	if status != http.StatusOK || result.Deleted == nil || contractfixture.DeletedSourceTextForTest(t, srv, p.ID, result.Deleted) != "retained\n" || result.Content != "" || result.Writable || result.SHA256 != "" {
		t.Fatalf("deleted read = %d %+v", status, result)
	}
	view := contractfixture.ComparisonViewForTest(t, srv, p.ID, wire.SourceComparisonSelector{Version: result.Deleted.Source}, "before")
	if reader := view.Comparison.Summary; reader.Before.Availability != "available" || reader.After.Availability != "absent" || reader.Removed != 1 || reader.Added != 0 {
		t.Fatalf("deleted reader must compare the retained state with its absence: %+v", reader)
	}
	oldFileID := result.FileID
	testutil.FailErr(t, "recreate path", os.WriteFile(filepath.Join(root, "gone.go"), []byte("current\n"), 0600))
	resolved = srv.Sources.Workspace.ResolveNavigationPaths(t.Context(), project, sourcebranch.Trunk, resolved)
	if resolved[0].Status != wire.NavigationResolved || resolved[0].Deleted {
		t.Fatalf("recreated navigation = %+v", resolved)
	}
	status, result = get(true)
	if status != http.StatusOK || result.Deleted != nil || result.Content != "current\n" || result.FileID == oldFileID {
		t.Fatalf("current read = %d %+v", status, result)
	}
}

func TestDeletedSourceKeepsContentAvailabilityAndScreening(t *testing.T) {
	for _, tc := range []struct {
		name   string
		before []byte
		sha    string
		want   string
	}{
		{name: "text", before: []byte("aws_key = AKIAQYJK5TXV4NZR7SGB\n"), want: "available"},
		{name: "empty", before: []byte{}, want: "available"},
		{name: "binary", before: []byte{0, 1, 0, 2, 0, 3}, want: "binary"},
		{name: "uncaptured", sha: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", want: "not_captured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
			testutil.FailErr(t, "build secret matcher", err)
			ledger, ledgerDB, withLedger := contractfixture.TestSourceLedger(t)
			srv := contractfixture.NewTestServer(t, withLedger, func(d *hostapi.Dependencies) { d.Approvals.SecretSpans = secretspan.New(matcher) })
			p := contractfixture.CreateProjectForTest(t, srv, t.TempDir())
			contractfixture.MirrorLedgerProject(t, ledgerDB, p)
			testutil.FailErr(t, "record deletion", ledger.Record(t.Context(), sourceledger.RecordInput{
				ProjectID: p.ID, RootID: p.Roots[0].ID, Path: "gone", Op: wire.SourceChangeOpDelete,
				Origin: wire.SourceChangeOriginUser, Before: tc.before, BeforeSHA256: tc.sha, BeforeSize: 30,
			}))
			q := url.Values{"path": {"gone"}, "root_id": {p.Roots[0].ID}, "include_deleted": {"true"}}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source?"+q.Encode(), nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("read = %d %s", rec.Code, rec.Body.String())
			}
			var result wire.ProjectSourceReadResponse
			testutil.FailErr(t, "decode deleted source", json.Unmarshal(rec.Body.Bytes(), &result))
			if result.Deleted == nil || result.Deleted.Previous.Availability != tc.want {
				t.Fatalf("availability = %+v", result.Deleted)
			}
			side := result.Deleted.Previous
			if tc.name == "text" {
				view := contractfixture.ComparisonViewForTest(t, srv, p.ID, wire.SourceComparisonSelector{Version: result.Deleted.Source}, "before")
				view = contractfixture.AwaitComparisonScreen(t, srv, p.ID, view.ID)
				rows := contractfixture.ComparisonRowsForTest(t, srv, p.ID, view, 0).Rows
				if side.SecretScreen == nil || rows[0].SecretScreen == nil || len(rows[0].SecretScreen.Spans) == 0 {
					t.Fatalf("missing historical secret screen: %+v", side)
				}
			} else if contractfixture.DeletedSourceTextForTest(t, srv, p.ID, result.Deleted) != "" {
				t.Fatalf("unreadable content exposed: %+v", side)
			}
		})
	}
}

func TestDeletedWorkerSourceDoesNotOpenThePrimaryReplacement(t *testing.T) {
	ledger, ledgerDB, withLedger := contractfixture.TestSourceLedger(t)
	queue := worker.NewInMemoryQueue(1)
	srv := contractfixture.NewTestServer(t, withLedger, contractfixture.WithWorkers(queue))
	primary, branchRoot := t.TempDir(), t.TempDir()
	p := contractfixture.CreateProjectForTest(t, srv, primary)
	contractfixture.MirrorLedgerProject(t, ledgerDB, p)
	rootID := p.Roots[0].ID
	testutil.FailErr(t, "primary file", os.WriteFile(filepath.Join(primary, "gone.go"), []byte("primary replacement\n"), 0600))
	testutil.FailErr(t, "worker metadata", workspace.WriteJobMeta(enginepaths.MetaDirForBranchRoot(branchRoot), workspace.JobMeta{
		Roots: []workspace.JobMetaRoot{{ID: rootID, Path: primary, IsPrimary: true}}, SnapshotComplete: true,
	}))
	workerID := contractfixture.EnqueueBranchJob(t, queue, p.ID, branchRoot)
	branch, err := sourcebranch.ForWorker(workerID)
	testutil.FailErr(t, "worker identity", err)
	testutil.FailErr(t, "record worker deletion", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID, BranchID: branch, RootID: rootID, Path: "gone.go", Op: wire.SourceChangeOpDelete,
		Origin: wire.SourceChangeOriginAgent, Before: []byte("worker before deletion\n"),
	}))
	project, err := srv.Sources.Workspace.ProjectRegistry.Get(t.Context(), p.ID)
	testutil.FailErr(t, "get project", err)
	resolved := srv.Admin.SessionAdmin.Navigation.ResolveNavigationJob(t.Context(), project, workerID, []wire.NavigationReference{{
		ID: "ref-0", ProjectID: p.ID, RootID: rootID, Path: "gone.go", Status: wire.NavigationResolved, EntryKind: "file", WorkerID: workerID,
	}})
	if len(resolved) != 1 || !resolved[0].Deleted || resolved[0].WorkerID != workerID {
		t.Fatalf("worker navigation = %+v", resolved)
	}
	q := url.Values{"root_id": {rootID}, "path": {"gone.go"}, "worker_id": {workerID}, "include_deleted": {"true"}}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source?"+q.Encode(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("worker read = %d %s", rec.Code, rec.Body.String())
	}
	var result wire.ProjectSourceReadResponse
	testutil.FailErr(t, "decode worker source", json.Unmarshal(rec.Body.Bytes(), &result))
	if result.WorkspaceKind != wire.SourceWorkspaceKindWorker || result.Deleted == nil || contractfixture.DeletedSourceTextForTest(t, srv, p.ID, result.Deleted) != "worker before deletion\n" || result.Content != "" {
		t.Fatalf("worker source = %+v", result)
	}
}
