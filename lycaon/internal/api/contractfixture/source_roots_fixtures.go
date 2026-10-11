package contractfixture

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/api/taskgroup"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/apitestdeps"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func CompleteBranchMeta(t *testing.T, branch string) {
	t.Helper()
	meta := workspace.JobMeta{
		Roots:            []workspace.JobMetaRoot{{ID: "primary", Path: t.TempDir(), IsPrimary: true}},
		SnapshotComplete: true,
	}
	testutil.FailErr(t, "write branch meta", workspace.WriteJobMeta(enginepaths.MetaDirForBranchRoot(branch), meta))
}

func DeleteProject(t *testing.T, srv *hostapi.Server, projectID string) {
	t.Helper()
	reqBody, err := json.Marshal(wire.ProjectRemovalRequest{OperationID: uuid.NewString()})
	testutil.FailErr(t, "encode removal request", err)
	req := NewAuthedRequest(http.MethodPost, "/v1/projects/"+projectID+"/removals", bytes.NewReader(reqBody))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("remove project status = %d body = %s", w.Code, w.Body.String())
	}
}

// newSnapshotTestServer serves a source ledger and keeps the project registry
// in the ledger's database.

func EnqueueBranchJob(t *testing.T, q *worker.InMemoryQueue, projectID, branchRoot string) string {
	t.Helper()
	id, err := q.EnqueueWithProjectID(context.Background(), projectID, wire.WorkerTask{
		Prompt:        "fixture",
		Brief:         "fixture",
		ProjectID:     projectID,
		WorkspacePath: t.TempDir(),
		WorkspaceRoot: branchRoot,
		Scope:         &wire.TaskScope{Mode: wire.TaskScopeModeWrite, Paths: []string{"."}},
	})
	testutil.FailErr(t, "enqueue", err)
	return id
}

func NewSnapshotTestServer(t *testing.T) *hostapi.Server {
	t.Helper()
	_, ledgerDB, withLedger := TestSourceLedger(t)
	// Production keeps the project registry and snapshots in one database, so
	// deletion also cascades the inventory row that may pin this snapshot.
	return NewTestServer(t, withLedger, func(d *hostapi.Dependencies) {
		d.Core.Projects = project.NewSQLRegistry(ledgerDB)
		if d.Core.Sessions != nil {
			d.Core.Sessions.SetProjectRegistry(d.Core.Projects)
		}
	})
}

func NewTestSources(t *testing.T, workers worker.WorkerQueue) *sourceapi.Handler {
	t.Helper()
	var deps apitestdeps.Deps
	apitestdeps.Fill(t, &deps)
	sources := sourceapi.New(&httpio.Responder{Logger: slog.Default()}, &taskgroup.Group{}, sourceapi.Operations{}, sourceapi.Deps{
		EditorDocuments: deps.EditorDocuments, FileBriefings: deps.FileBriefings, ManagedSecrets: deps.ManagedSecrets,
		MutationGate: deps.MutationGate, ProjectRegistry: deps.Projects, SessionStore: deps.Store,
		SourceLedger: deps.SourceLedger, SourceInventory: deps.SourceLedger.Inventory, SourceMutations: deps.SourceMutations, FileOperations: deps.FileOperations,
		Workers: workers,
	})
	return &sources
}

func PublishSnapshotFor(t *testing.T, srv *hostapi.Server, root string) {
	t.Helper()
	store := srv.Sources.Workspace.SourceLedger.Snapshots
	if store == nil {
		t.Fatal("test server has no snapshot store")
	}
	_, err := store.Ensure(t.Context(), sourcesnapshot.Request{
		Roots: []sourcesnapshot.Root{{Path: root}},
	})
	testutil.FailErr(t, "publish snapshot", err)
}

func SeedProjectWithSnapshot(t *testing.T, srv *hostapi.Server, root string) wire.Project {
	t.Helper()
	p := CreateProjectForTest(t, srv, root)
	DrainBackground(t, srv)
	PublishSnapshotFor(t, srv, p.Roots[0].Path)
	return p
}

// Deleting a project releases its snapshots and the content they pin.

func SnapshotCount(t *testing.T, srv *hostapi.Server) int {
	t.Helper()
	var count int
	testutil.FailErr(t, "count snapshots", srv.Sources.Workspace.SourceLedger.LedgerDB().
		QueryRowContext(t.Context(), `SELECT COUNT(*) FROM source_snapshots`).Scan(&count))
	return count
}

type SwitchingProjectRegistry struct {
	project.Registry
	First  *project.Project
	Second *project.Project
	Armed  atomic.Bool
	Gets   atomic.Int32
}

func (r *SwitchingProjectRegistry) Arm(first, second *project.Project) {
	r.First, r.Second = first, second
	r.Armed.Store(true)
}

func (r *SwitchingProjectRegistry) Get(ctx context.Context, id string) (*project.Project, error) {
	if !r.Armed.Load() {
		return r.Registry.Get(ctx, id)
	}
	if r.Gets.Add(1) == 1 {
		return r.First, nil
	}
	return r.Second, nil
}
