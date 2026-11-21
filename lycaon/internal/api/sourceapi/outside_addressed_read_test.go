//go:build darwin || linux

package sourceapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestOutsideMetadataAndImageReadsRequireHeldIdentity(t *testing.T) {
	db := testdbfixture.Open(t, "store.db")
	sessions := store.NewSQL(db)
	server := newSourceHandlerFixture(t, func(deps *Deps) {
		deps.SessionStore, deps.ProjectRegistry = sessions, project.NewSQLRegistry(db)
		deps.SourceLedger = sourceledger.New(db, t.TempDir())
	})
	p := sourceViewFixtureProject(t, server)
	ctx := testdbseed.OwnerCaller(t, t.Context(), db)
	chat, err := sessions.Create(ctx, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create chat", err)
	dir := filepath.Join(t.TempDir(), "observed")
	testutil.FailErr(t, "make observed folder", os.Mkdir(dir, 0o700))
	image := filepath.Join(dir, "diagram.svg")
	const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="2" height="2"><rect width="2" height="2"/></svg>`
	testutil.FailErr(t, "write outside image", os.WriteFile(image, []byte(svg), 0o600))
	opened := readOpenedFile(t, callSourceViewHandlerAs(t, ctx, server.Workspace.HandleOpenOutsideFile, p.ID, "",
		wire.OpenOutsideFileRequest{SessionID: chat.ID, Path: image}))

	router := chi.NewRouter()
	router.Get("/projects/{id}/source", server.Workspace.HandleGetProjectSource)
	router.Get("/projects/{id}/source/raw", server.Workspace.HandleGetProjectSourceRaw)
	read := func(endpoint string, query url.Values) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/projects/"+p.ID+"/source"+endpoint+"?"+query.Encode(), nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	pathQuery := url.Values{"root_id": {opened.RootID}, "path": {opened.Path}}
	idQuery := url.Values{"file_id": {opened.FileID}}
	for _, endpoint := range []string{"", "/raw"} {
		if code := sourceViewErrorCode(t, read(endpoint, pathQuery), http.StatusForbidden); code != wire.ApiErrorCodeSourcePathDenied {
			t.Fatalf("outside path read returned %q", code)
		}
		sourceViewErrorCode(t, read(endpoint, url.Values{"file_id": {uuid.NewString()}}), http.StatusNotFound)
		sourceViewErrorCode(t, read(endpoint, url.Values{"file_id": {opened.FileID}, "path": {image}}), http.StatusBadRequest)
		if response := read(endpoint, idQuery); response.Code != http.StatusOK {
			t.Fatalf("held read %q = %d %s", endpoint, response.Code, response.Body.String())
		}
	}
	if raw := read("/raw", idQuery); raw.Body.String() != svg || raw.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("held image = %q, %q", raw.Header().Get("Content-Type"), raw.Body.String())
	}

	testutil.FailErr(t, "move observed folder", os.Rename(dir, dir+"-old"))
	testutil.FailErr(t, "replace observed folder", os.Mkdir(dir, 0o700))
	testutil.FailErr(t, "write replacement image", os.WriteFile(image, []byte(svg), 0o600))
	sourceViewErrorCode(t, read("", idQuery), http.StatusForbidden)
	sourceViewErrorCode(t, read("/raw", idQuery), http.StatusForbidden)
}
