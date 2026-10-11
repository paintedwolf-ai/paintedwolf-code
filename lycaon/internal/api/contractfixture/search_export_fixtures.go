package contractfixture

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	catalogtest "github.com/lycaon/lycaon/internal/testsetup/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func NewSearchExportServer(t *testing.T) (db.Handle, *hostapi.Server) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	roots := map[string]sourcecatalog.Root{}
	for _, projectID := range []string{"proj-a", "proj-b"} {
		dir := t.TempDir()
		roots[projectID] = sourcecatalog.Root{ID: testdbseed.InsertProjectRoot(t, sqlDB, projectID, dir), Path: dir}
	}
	store := store.NewSQL(sqlDB)
	reg := project.NewSQLRegistry(sqlDB)
	srv := hostapi.NewServer(RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: store, Projects: reg}}), nil, hostapi.TestAPIToken)
	// Unscoped exports also search every attached root's source; an unsettled
	// root makes the code leg report partial coverage, which export marks truncated.
	for projectID, root := range roots {
		testutil.FailErr(t, "settle source inventory for "+projectID,
			catalogtest.AwaitIndex(t.Context(), sourcecatalog.Process(), projectID, root))
	}
	return sqlDB, srv
}

func PostSearchExport(t *testing.T, srv *hostapi.Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := NewAuthedRequest(http.MethodPost, "/v1/search/export", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}
