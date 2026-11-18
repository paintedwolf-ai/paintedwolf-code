package contractfixture

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func GetSourceAttribution(t *testing.T, srv *hostapi.Server, projectID string, q url.Values) int {
	t.Helper()
	req := NewAuthedRequest(http.MethodGet,
		"/v1/projects/"+projectID+"/source/attribution?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w.Code
}

// Attribution resolves session scope like every other source read.

func GetSourceWalk(t *testing.T, srv *hostapi.Server, projectID string, query url.Values) (*httptest.ResponseRecorder, wire.SourceWalkResponse) {
	t.Helper()
	req := NewAuthedRequest(http.MethodGet, "/v1/projects/"+projectID+"/source/walk?"+query.Encode(), nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var page wire.SourceWalkResponse
	if w.Code == http.StatusOK {
		testutil.FailErr(t, "decode walk", json.Unmarshal(w.Body.Bytes(), &page))
	}
	return w, page
}

// `turn:{session}` reads the chat's current turn and names the ordinal it read.

func PrepareLedgerInventory(t *testing.T, srv *hostapi.Server, ledger *sourceledger.Store, p wire.Project) {
	t.Helper()
	DrainBackground(t, srv)
	roots := make([]sourceledger.RootSpec, 0, len(p.Roots))
	for _, root := range p.Roots {
		roots = append(roots, sourceledger.RootSpec{ID: root.ID, Path: root.Path})
	}
	testutil.FailErr(t, "prepare source inventory", ledger.Inventory.EnsureInventory(t.Context(), sourceledger.InventoryRequest{
		ProjectID: p.ID, RootsGeneration: p.RootsGeneration, Roots: roots,
	}))
}

func TestSourceLedger(t *testing.T) (*sourceledger.Store, *db.Store, TestDeps) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	st := sourceledger.New(sqlDB, t.TempDir())
	return st, sqlDB, func(d *hostapi.Dependencies) {
		WithSessionStore(sessionstore.NewSQL(sqlDB))(d)
		d.Source.SourceLedger, d.Source.SourceInventory = st, st.Inventory
		d.Source.SourceMutations = projectsource.NewSourceMutationService(sqlDB, st)
	}
}

func WithSessionStore(store *sessionstore.Repositories) TestDeps {
	return func(d *hostapi.Dependencies) {
		d.Core.Store = store
		d.Core.Database = store.DB()
		d.Core.Projects = project.NewSQLRegistry(store.DB())
	}
}

// withWorkers serves worker jobs from queue.

func WithWorkers(queue worker.WorkerQueue) TestDeps {
	return func(d *hostapi.Dependencies) { d.Workflow.Workers = queue }
}
