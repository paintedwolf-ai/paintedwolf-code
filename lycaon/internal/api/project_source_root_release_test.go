package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func publishSnapshotFor(t *testing.T, srv *Server, root string) {
	t.Helper()
	store := srv.Sources.SourceLedger.SnapshotStore()
	if store == nil {
		t.Fatal("test server has no snapshot store")
	}
	_, err := store.Ensure(t.Context(), sourcesnapshot.Request{
		Roots: []sourcesnapshot.Root{{Path: root}},
	})
	testutil.FailErr(t, "publish snapshot", err)
}

func snapshotCount(t *testing.T, srv *Server) int {
	t.Helper()
	var count int
	testutil.FailErr(t, "count snapshots", srv.Sources.SourceLedger.LedgerDB().
		QueryRowContext(t.Context(), `SELECT COUNT(*) FROM source_snapshots`).Scan(&count))
	return count
}

func deleteProject(t *testing.T, srv *Server, projectID string) {
	t.Helper()
	reqBody, err := json.Marshal(wire.ProjectRemovalRequest{OperationID: uuid.NewString()})
	testutil.FailErr(t, "encode removal request", err)
	req := newAuthedRequest(http.MethodPost, "/v1/projects/"+projectID+"/removals", bytes.NewReader(reqBody))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("remove project status = %d body = %s", w.Code, w.Body.String())
	}
}

// newSnapshotTestServer serves a source ledger and keeps the project registry
// in the ledger's database.
func newSnapshotTestServer(t *testing.T) *Server {
	t.Helper()
	_, ledgerDB, withLedger := testSourceLedger(t)
	// Production keeps the project registry and snapshots in one database, so
	// deletion also cascades the inventory row that may pin this snapshot.
	return newTestServer(t, withLedger, func(d *Dependencies) {
		d.Projects = project.NewSQLRegistry(ledgerDB)
		if d.Sessions != nil {
			d.Sessions.SetProjectRegistry(d.Projects)
		}
	})
}

func seedProjectWithSnapshot(t *testing.T, srv *Server, root string) wire.Project {
	t.Helper()
	p := createProjectForTest(t, srv, root)
	drainBackground(t, srv)
	publishSnapshotFor(t, srv, p.Roots[0].Path)
	return p
}

// Deleting a project releases its snapshots and the content they pin.
func TestDeletingAProjectReleasesItsSourceSnapshots(t *testing.T) {
	srv := newSnapshotTestServer(t)
	root := t.TempDir()
	testutil.FailErr(t, "write source",
		os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644))

	p := seedProjectWithSnapshot(t, srv, root)
	if snapshotCount(t, srv) == 0 {
		t.Fatal("no snapshot to release")
	}
	deleteProject(t, srv, p.ID)

	if got := snapshotCount(t, srv); got != 0 {
		t.Fatalf("snapshots after project delete = %d want 0", got)
	}
}

// Two projects over one tree share its snapshots. Deleting one must not take
// the other's history with it.
func TestDeletingOneProjectKeepsASharedRootsSnapshots(t *testing.T) {
	srv := newSnapshotTestServer(t)
	root := t.TempDir()
	testutil.FailErr(t, "write source",
		os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644))

	first := seedProjectWithSnapshot(t, srv, root)
	second := createProjectForTest(t, srv, root)
	if second.ID == first.ID {
		t.Fatal("expected a second project over the same root")
	}

	deleteProject(t, srv, first.ID)

	if got := snapshotCount(t, srv); got == 0 {
		t.Fatal("shared root lost its snapshots when one of its projects was deleted")
	}
}
