package searchadmin

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	catalogtest "github.com/lycaon/lycaon/internal/testsetup/sourcecatalog"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// TestMain installs process-global test dependencies.
func TestMain(m *testing.M) {
	gittestsetup.Enable()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	os.Exit(m.Run())
}

// searchFixture serves the package's handlers over a real database, project
// registry, and source mutation service.
type searchFixture struct {
	database     *db.Store
	projects     *project.SQLRegistry
	handler      Handler
	router       http.Handler
	sourceErrors []error
}

func newSearchFixture(t *testing.T) *searchFixture {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	f := &searchFixture{database: database, projects: project.NewSQLRegistry(database)}
	ledger := sourceledger.New(database, t.TempDir())
	responses := &httpio.Responder{Logger: slog.New(slog.DiscardHandler)}
	f.handler = New(responses, Dependencies{
		Database:        database,
		Projects:        f.projects,
		SourceMutations: projectsource.NewSourceMutationService(database, ledger),
		ChatAffiliation: func(*http.Request) (string, int) { return "", 0 },
		WriteSourceError: func(w http.ResponseWriter, _ *http.Request, err error) {
			f.sourceErrors = append(f.sourceErrors, err)
			responses.Fail(w, wire.ApiErrorCodeSourceWriteConflict, "source mutation refused")
		},
	})
	f.router = f.mount(f.handler)
	return f
}

// mount registers the handlers on the routes the API server declares.
func (f *searchFixture) mount(h Handler) http.Handler {
	r := chi.NewRouter()
	r.Post("/v1/search", h.HandleSearch)
	r.Post("/v1/search/export", h.HandleExportSearchResults)
	r.Post("/v1/search/replace/preview", h.HandlePreviewSearchReplacement)
	r.Post("/v1/search/replace/apply", h.HandleApplySearchReplacement)
	return r
}

// addProject attaches a settled folder root holding files.
func (f *searchFixture) addProject(t *testing.T, name, label string, files map[string]string) *project.Project {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		testutil.FailErr(t, "write "+rel, os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o600))
	}
	p, err := f.projects.Create(t.Context(), project.CreateParams{
		Name:  name,
		Roots: []project.AttachRootParams{{Path: dir, Label: label}},
	})
	testutil.FailErr(t, "create project "+name, err)
	root := sourcecatalog.Root{ID: p.Roots[0].ID, Path: p.Roots[0].Path}
	testutil.FailErr(t, "settle source inventory for "+name,
		catalogtest.AwaitIndex(t.Context(), sourcecatalog.Process(), p.ID, root))
	return p
}

func (f *searchFixture) seedRow(t *testing.T, row search.IndexRow) {
	t.Helper()
	if row.TS == "" {
		row.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	tx, err := f.database.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin search index tx", err)
	defer func() { _ = tx.Rollback() }()
	testutil.FailErr(t, "upsert search row", search.NewStore().UpsertRows(t.Context(), tx, []search.IndexRow{row}))
	testutil.FailErr(t, "commit search index tx", tx.Commit())
}

func (f *searchFixture) post(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return postTo(t, f.router, path, body)
}

func postTo(t *testing.T, router http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func jsonBody(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	testutil.FailErr(t, "encode request", err)
	return string(raw)
}

func decodeOK[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var out T
	testutil.FailErr(t, "decode response", json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

// requireErrorCode asserts the structured refusal code and its declared status.
func requireErrorCode(t *testing.T, rec *httptest.ResponseRecorder, want wire.ApiErrorCode) wire.ErrorResponse {
	t.Helper()
	var resp wire.ErrorResponse
	testutil.FailErr(t, "decode error response", json.Unmarshal(rec.Body.Bytes(), &resp))
	if resp.Code != want || rec.Code != want.HTTPStatus() {
		t.Fatalf("status = %d code = %q, want %d %q; body=%s", rec.Code, resp.Code, want.HTTPStatus(), want, rec.Body.String())
	}
	return resp
}
