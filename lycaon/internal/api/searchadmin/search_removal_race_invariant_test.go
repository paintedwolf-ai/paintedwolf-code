package searchadmin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type spyProjectRegistry struct {
	project.Registry
	listCalls atomic.Int32
	listExtra []project.Project
	getErrFor map[string]error
	listErr   error
}

func (s *spyProjectRegistry) Get(ctx context.Context, id string) (*project.Project, error) {
	if err, ok := s.getErrFor[id]; ok {
		return nil, err
	}
	return s.Registry.Get(ctx, id)
}

func (s *spyProjectRegistry) List(ctx context.Context) ([]project.Project, error) {
	s.listCalls.Add(1)
	if s.listErr != nil {
		return nil, s.listErr
	}
	projects, err := s.Registry.List(ctx)
	if err != nil {
		return nil, err
	}
	return append(projects, s.listExtra...), nil
}

func mountWithRegistry(t *testing.T, f *searchFixture, reg project.Registry) http.Handler {
	t.Helper()
	ledger := sourceledger.New(f.database, t.TempDir(), sourceledger.Timing{})
	responses := &httpio.Responder{Logger: slog.New(slog.DiscardHandler)}
	h := New(responses, Dependencies{
		Database:        f.database,
		Projects:        reg,
		SourceMutations: projectsource.NewSourceMutationService(f.database, ledger),
		ChatAffiliation: func(*http.Request) (string, int) { return "", 0 },
		WriteSourceError: func(w http.ResponseWriter, _ *http.Request, err error) {
			responses.Fail(w, wire.ApiErrorCodeSourceWriteConflict, err.Error())
		},
	})
	return f.mount(h)
}

func TestSearchExportReplaceSurviveUnrelatedProjectMidRemoval(t *testing.T) {
	f := newSearchFixture(t)
	const origBody = "package main\n\nconst Token = \"needle_old\"\n"
	const wantBody = "package main\n\nconst Token = \"needle_new\"\n"
	healthy := f.addProject(t, "Healthy", "healthy-root", map[string]string{
		"main.go": origBody,
	})
	removing := f.addProject(t, "Removing", "removing-root", map[string]string{
		"gone.go": "package gone\n\nconst Token = \"needle_old\"\n",
	})

	// Deterministically place `removing` in the mid-Delete state where child
	// rows (project_roots including outside namespace) are gone while the
	// projects row still exists.
	testutil.FailErr(t, "delete removing project rows", db.DeleteProjectRows(t.Context(), f.database, removing.ID))

	// 1. Origin-scoped, explicit healthy-slug, and global search return exact
	// healthy project/root/path hits and zero results belonging to `removing`.
	for name, req := range map[string]wire.SearchRequest{
		"origin_scoped": {Query: "needle_old", OriginProjectID: healthy.ID},
		"healthy_slug":  {Query: "project:healthy needle_old", OriginProjectID: healthy.ID},
		"global":        {Query: "needle_old"},
	} {
		resp := decodeOK[wire.SearchResponse](t, f.post(t, "/v1/search", jsonBody(t, req)))
		if len(resp.Hits) != 1 {
			t.Fatalf("%s search hits = %+v, want exactly 1 healthy hit", name, resp.Hits)
		}
		h := resp.Hits[0]
		if h.ProjectID != healthy.ID || h.RootID != healthy.Roots[0].ID || h.Path != "main.go" {
			t.Fatalf("%s hit = %+v, want project=%s root=%s path=main.go", name, h, healthy.ID, healthy.Roots[0].ID)
		}
	}

	// 2. Export succeeds with healthy project's exact hit and zero hits from `removing`.
	exportRec := f.post(t, "/v1/search/export", jsonBody(t, wire.SearchExportRequest{
		Query: "needle_old", Format: wire.SearchExportFormatJSONL, OriginProjectID: healthy.ID,
	}))
	if exportRec.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", exportRec.Code, exportRec.Body.String())
	}
	exportBody := exportRec.Body.String()
	if !strings.Contains(exportBody, `"path":"main.go"`) || !strings.Contains(exportBody, healthy.ID) || strings.Contains(exportBody, removing.ID) {
		t.Fatalf("unexpected export body=%s", exportBody)
	}

	// 3. Replace preview (origin-scoped and global) returns exact healthy file and zero `removing` files.
	preview := decodeOK[wire.SearchReplacePreviewResponse](t, f.post(t, "/v1/search/replace/preview", jsonBody(t, wire.SearchReplacePreviewRequest{
		Query: "needle_old", Replacement: "needle_new", OriginProjectID: healthy.ID,
	})))
	if preview.State != wire.SearchReplacePreviewState(search.ReplacePreviewReady) || len(preview.Files) != 1 ||
		preview.Files[0].RootID != healthy.Roots[0].ID || preview.Files[0].Path != "main.go" {
		t.Fatalf("origin preview = %+v", preview)
	}
	globalPreview := decodeOK[wire.SearchReplacePreviewResponse](t, f.post(t, "/v1/search/replace/preview", jsonBody(t, wire.SearchReplacePreviewRequest{
		Query: "needle_old", Replacement: "needle_new",
	})))
	if globalPreview.State != wire.SearchReplacePreviewState(search.ReplacePreviewReady) || len(globalPreview.Files) != 1 ||
		globalPreview.Files[0].RootID != healthy.Roots[0].ID || globalPreview.Files[0].Path != "main.go" {
		t.Fatalf("global preview = %+v", globalPreview)
	}

	// 4. Replace apply writes verbatim expected bytes to healthy project's file.
	pf := preview.Files[0]
	applyResp := decodeOK[wire.SearchReplaceApplyResponse](t, f.post(t, "/v1/search/replace/apply", jsonBody(t, wire.SearchReplaceApplyRequest{
		OperationID: uuid.NewString(), Query: "needle_old", Replacement: "needle_new", OriginProjectID: healthy.ID,
		Files: []wire.SearchReplaceApplyFile{{RootID: pf.RootID, Path: pf.Path, SHA256: pf.SHA256, Hunks: []int{0}}},
	})))
	if len(applyResp.Files) != 1 || !applyResp.Files[0].Applied || applyResp.Files[0].RootID != healthy.Roots[0].ID || applyResp.Files[0].Path != "main.go" {
		t.Fatalf("apply response = %+v", applyResp)
	}
	written, err := os.ReadFile(filepath.Join(healthy.Roots[0].Path, "main.go"))
	testutil.FailErr(t, "read applied file", err)
	if string(written) != wantBody {
		t.Fatalf("applied file = %q, want verbatim %q", written, wantBody)
	}

	// 5. Explicit slug for the mid-removal project fails cleanly as query invalid (400), not 500.
	requireErrorCode(t, f.post(t, "/v1/search", jsonBody(t, wire.SearchRequest{
		Query: "project:removing needle_old", OriginProjectID: healthy.ID,
	})), wire.ApiErrorCodeSearchQueryInvalid)
}

func TestSearchExportReplaceTolerateTOCTOUVanishedProjectBetweenListAndGet(t *testing.T) {
	f := newSearchFixture(t)
	healthy := f.addProject(t, "Healthy", "healthy-root", map[string]string{
		"app.go": "package app\n\nconst Marker = \"toctou_hit\"\n",
	})
	vanishedID := uuid.NewString()
	spy := &spyProjectRegistry{
		Registry:  f.projects,
		listExtra: []project.Project{{ID: vanishedID, Name: "Vanished"}},
		getErrFor: map[string]error{vanishedID: project.ErrNotFound},
	}
	router := mountWithRegistry(t, f, spy)

	searchResp := decodeOK[wire.SearchResponse](t, postTo(t, router, "/v1/search", jsonBody(t, wire.SearchRequest{
		Query: "toctou_hit",
	})))
	if len(searchResp.Hits) != 1 || searchResp.Hits[0].ProjectID != healthy.ID ||
		searchResp.Hits[0].RootID != healthy.Roots[0].ID || searchResp.Hits[0].Path != "app.go" {
		t.Fatalf("global search with TOCTOU vanished project = %+v", searchResp)
	}

	exportRec := postTo(t, router, "/v1/search/export", jsonBody(t, wire.SearchExportRequest{
		Query: "toctou_hit", Format: wire.SearchExportFormatJSONL,
	}))
	if exportRec.Code != http.StatusOK || !strings.Contains(exportRec.Body.String(), `"path":"app.go"`) || strings.Contains(exportRec.Body.String(), vanishedID) {
		t.Fatalf("export with TOCTOU vanished project status=%d body=%s", exportRec.Code, exportRec.Body.String())
	}

	preview := decodeOK[wire.SearchReplacePreviewResponse](t, postTo(t, router, "/v1/search/replace/preview", jsonBody(t, wire.SearchReplacePreviewRequest{
		Query: "toctou_hit", Replacement: "toctou_done",
	})))
	if preview.State != wire.SearchReplacePreviewState(search.ReplacePreviewReady) || len(preview.Files) != 1 ||
		preview.Files[0].RootID != healthy.Roots[0].ID || preview.Files[0].Path != "app.go" {
		t.Fatalf("global replace preview with TOCTOU vanished project = %+v", preview)
	}
}

func TestSearchPreservesGlobalSearchSlugResolutionOriginSkipAndGenuineErrors(t *testing.T) {
	f := newSearchFixture(t)
	alpha := f.addProject(t, "Alpha", "alpha-root", map[string]string{
		"a.go": "package a\n\nconst V = \"shared_tok\"\n",
	})
	beta := f.addProject(t, "Beta", "beta-root", map[string]string{
		"b.go": "package b\n\nconst V = \"shared_tok\"\n",
	})

	spy := &spyProjectRegistry{Registry: f.projects}
	router := mountWithRegistry(t, f, spy)

	// Global search, global export, and global replace preview enumerate all active projects.
	global := decodeOK[wire.SearchResponse](t, postTo(t, router, "/v1/search", jsonBody(t, wire.SearchRequest{
		Query: "shared_tok",
	})))
	if len(global.Hits) != 2 {
		t.Fatalf("global search hits = %d, want 2", len(global.Hits))
	}
	globalPrev := decodeOK[wire.SearchReplacePreviewResponse](t, postTo(t, router, "/v1/search/replace/preview", jsonBody(t, wire.SearchReplacePreviewRequest{
		Query: "shared_tok", Replacement: "next_tok",
	})))
	if len(globalPrev.Files) != 2 {
		t.Fatalf("global replace preview files = %d, want 2", len(globalPrev.Files))
	}

	// Explicit slug resolves beta.
	slugged := decodeOK[wire.SearchResponse](t, postTo(t, router, "/v1/search", jsonBody(t, wire.SearchRequest{
		Query: "project:beta shared_tok", OriginProjectID: alpha.ID,
	})))
	if len(slugged.Hits) != 1 || slugged.Hits[0].ProjectID != beta.ID || slugged.Hits[0].Path != "b.go" {
		t.Fatalf("slugged search = %+v, want only beta/b.go", slugged.Hits)
	}

	// Origin-scoped replace preview AND apply avoid enumerating unrelated projects via List()
	// while still honoring explicit project:<slug> filters.
	spy.listCalls.Store(0)
	originPrev := decodeOK[wire.SearchReplacePreviewResponse](t, postTo(t, router, "/v1/search/replace/preview", jsonBody(t, wire.SearchReplacePreviewRequest{
		Query: "shared_tok", Replacement: "next_tok", OriginProjectID: alpha.ID,
	})))
	if len(originPrev.Files) != 1 || originPrev.Files[0].RootID != alpha.Roots[0].ID || originPrev.Files[0].Path != "a.go" {
		t.Fatalf("origin replace preview = %+v", originPrev)
	}
	if got := spy.listCalls.Load(); got != 0 {
		t.Fatalf("origin-scoped replace preview called List() %d times, want 0", got)
	}

	// Explicit project:beta on origin=alpha preview returns empty (confined to origin) and apply refuses with 400.
	crossPrev := decodeOK[wire.SearchReplacePreviewResponse](t, postTo(t, router, "/v1/search/replace/preview", jsonBody(t, wire.SearchReplacePreviewRequest{
		Query: "project:beta shared_tok", Replacement: "next_tok", OriginProjectID: alpha.ID,
	})))
	if len(crossPrev.Files) != 0 {
		t.Fatalf("cross-project explicit slug preview = %+v, want 0 files", crossPrev.Files)
	}
	requireErrorCode(t, postTo(t, router, "/v1/search/replace/apply", jsonBody(t, wire.SearchReplaceApplyRequest{
		OperationID: uuid.NewString(), Query: "project:beta shared_tok", Replacement: "next_tok", OriginProjectID: alpha.ID,
		Files: []wire.SearchReplaceApplyFile{},
	})), wire.ApiErrorCodeInvalidRequest)

	// Origin-scoped apply without project:<slug> makes 0 List() calls and writes verbatim bytes.
	spy.listCalls.Store(0)
	pf := originPrev.Files[0]
	applyResp := decodeOK[wire.SearchReplaceApplyResponse](t, postTo(t, router, "/v1/search/replace/apply", jsonBody(t, wire.SearchReplaceApplyRequest{
		OperationID: uuid.NewString(), Query: "shared_tok", Replacement: "next_tok", OriginProjectID: alpha.ID,
		Files: []wire.SearchReplaceApplyFile{{RootID: pf.RootID, Path: pf.Path, SHA256: pf.SHA256, Hunks: []int{0}}},
	})))
	if len(applyResp.Files) != 1 || !applyResp.Files[0].Applied {
		t.Fatalf("origin apply = %+v", applyResp)
	}
	if got := spy.listCalls.Load(); got != 0 {
		t.Fatalf("origin-scoped replace apply called List() %d times, want 0", got)
	}
	gotAlpha, err := os.ReadFile(filepath.Join(alpha.Roots[0].Path, "a.go"))
	testutil.FailErr(t, "read alpha a.go", err)
	if string(gotAlpha) != "package a\n\nconst V = \"next_tok\"\n" {
		t.Fatalf("alpha a.go = %q", gotAlpha)
	}

	// Genuine registry errors (not ErrNotFound) must still propagate as 500.
	failSpy := &spyProjectRegistry{
		Registry:  f.projects,
		getErrFor: map[string]error{beta.ID: errors.New("sqlite disk I/O error")},
	}
	failRouter := mountWithRegistry(t, f, failSpy)
	rec := postTo(t, failRouter, "/v1/search", jsonBody(t, wire.SearchRequest{Query: "shared_tok"}))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("genuine Get error status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}

	listFailSpy := &spyProjectRegistry{
		Registry: f.projects,
		listErr:  errors.New("sqlite database locked"),
	}
	listFailRouter := mountWithRegistry(t, f, listFailSpy)
	rec2 := postTo(t, listFailRouter, "/v1/search", jsonBody(t, wire.SearchRequest{Query: "shared_tok"}))
	if rec2.Code != http.StatusInternalServerError {
		t.Fatalf("genuine List error status = %d, want 500; body=%s", rec2.Code, rec2.Body.String())
	}
}
