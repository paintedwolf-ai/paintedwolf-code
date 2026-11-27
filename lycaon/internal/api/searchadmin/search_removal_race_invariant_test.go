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
	healthy := f.addProject(t, "Healthy", "healthy-root", map[string]string{
		"main.go": "package main\n\nconst Token = \"needle_old\"\n",
	})
	removing := f.addProject(t, "Removing", "removing-root", map[string]string{
		"gone.go": "package gone\n\nconst Token = \"needle_old\"\n",
	})

	// Deterministically place `removing` in the mid-Delete state where child
	// rows (project_roots including outside namespace) are gone while the
	// projects row still exists.
	testutil.FailErr(t, "delete removing project rows", db.DeleteProjectRows(t.Context(), f.database, removing.ID))

	// 1. Search (origin-scoped and global) succeeds for healthy project.
	scopedSearch := decodeOK[wire.SearchResponse](t, f.post(t, "/v1/search", jsonBody(t, wire.SearchRequest{
		Query: "needle_old", OriginProjectID: healthy.ID,
	})))
	if len(scopedSearch.Hits) == 0 {
		t.Fatalf("scoped search returned no hits during unrelated mid-removal: %+v", scopedSearch)
	}
	globalSearch := decodeOK[wire.SearchResponse](t, f.post(t, "/v1/search", jsonBody(t, wire.SearchRequest{
		Query: "needle_old",
	})))
	if len(globalSearch.Hits) == 0 {
		t.Fatalf("global search returned no hits during unrelated mid-removal: %+v", globalSearch)
	}

	// 2. Export succeeds for healthy project.
	exportRec := f.post(t, "/v1/search/export", jsonBody(t, wire.SearchExportRequest{
		Query: "needle_old", Format: wire.SearchExportFormatJSONL, OriginProjectID: healthy.ID,
	}))
	if exportRec.Code != http.StatusOK || !strings.Contains(exportRec.Body.String(), "main.go") {
		t.Fatalf("export status=%d body=%s", exportRec.Code, exportRec.Body.String())
	}

	// 3. Replace preview (origin-scoped and global) succeeds for healthy project.
	preview := decodeOK[wire.SearchReplacePreviewResponse](t, f.post(t, "/v1/search/replace/preview", jsonBody(t, wire.SearchReplacePreviewRequest{
		Query: "needle_old", Replacement: "needle_new", OriginProjectID: healthy.ID,
	})))
	if preview.State != wire.SearchReplacePreviewState(search.ReplacePreviewReady) || len(preview.Files) != 1 || preview.Files[0].Path != "main.go" {
		t.Fatalf("origin preview = %+v", preview)
	}
	globalPreview := decodeOK[wire.SearchReplacePreviewResponse](t, f.post(t, "/v1/search/replace/preview", jsonBody(t, wire.SearchReplacePreviewRequest{
		Query: "needle_old", Replacement: "needle_new",
	})))
	if globalPreview.State != wire.SearchReplacePreviewState(search.ReplacePreviewReady) || len(globalPreview.Files) != 1 {
		t.Fatalf("global preview = %+v", globalPreview)
	}

	// 4. Replace apply succeeds and writes healthy project's file.
	pf := preview.Files[0]
	applyResp := decodeOK[wire.SearchReplaceApplyResponse](t, f.post(t, "/v1/search/replace/apply", jsonBody(t, wire.SearchReplaceApplyRequest{
		OperationID: uuid.NewString(), Query: "needle_old", Replacement: "needle_new", OriginProjectID: healthy.ID,
		Files: []wire.SearchReplaceApplyFile{{RootID: pf.RootID, Path: pf.Path, SHA256: pf.SHA256, Hunks: []int{0}}},
	})))
	if len(applyResp.Files) != 1 || !applyResp.Files[0].Applied {
		t.Fatalf("apply response = %+v", applyResp)
	}
	written, err := os.ReadFile(filepath.Join(healthy.Roots[0].Path, "main.go"))
	testutil.FailErr(t, "read applied file", err)
	if !strings.Contains(string(written), "needle_new") {
		t.Fatalf("applied file = %q", written)
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
	if len(searchResp.Hits) != 1 || searchResp.Hits[0].ProjectID != healthy.ID {
		t.Fatalf("global search with TOCTOU vanished project = %+v", searchResp)
	}

	exportRec := postTo(t, router, "/v1/search/export", jsonBody(t, wire.SearchExportRequest{
		Query: "toctou_hit", Format: wire.SearchExportFormatJSONL,
	}))
	if exportRec.Code != http.StatusOK || !strings.Contains(exportRec.Body.String(), "app.go") {
		t.Fatalf("export with TOCTOU vanished project status=%d body=%s", exportRec.Code, exportRec.Body.String())
	}

	preview := decodeOK[wire.SearchReplacePreviewResponse](t, postTo(t, router, "/v1/search/replace/preview", jsonBody(t, wire.SearchReplacePreviewRequest{
		Query: "toctou_hit", Replacement: "toctou_done",
	})))
	if preview.State != wire.SearchReplacePreviewState(search.ReplacePreviewReady) || len(preview.Files) != 1 {
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

	// Global search returns hits from both projects.
	global := decodeOK[wire.SearchResponse](t, postTo(t, router, "/v1/search", jsonBody(t, wire.SearchRequest{
		Query: "shared_tok",
	})))
	if len(global.Hits) != 2 {
		t.Fatalf("global search hits = %d, want 2", len(global.Hits))
	}

	// Explicit slug resolves beta.
	slugged := decodeOK[wire.SearchResponse](t, postTo(t, router, "/v1/search", jsonBody(t, wire.SearchRequest{
		Query: "project:beta shared_tok", OriginProjectID: alpha.ID,
	})))
	if len(slugged.Hits) != 1 || slugged.Hits[0].ProjectID != beta.ID {
		t.Fatalf("slugged search = %+v, want only beta", slugged.Hits)
	}

	// Single-project replace preview avoids enumerating unrelated projects via List().
	spy.listCalls.Store(0)
	originPrev := decodeOK[wire.SearchReplacePreviewResponse](t, postTo(t, router, "/v1/search/replace/preview", jsonBody(t, wire.SearchReplacePreviewRequest{
		Query: "shared_tok", Replacement: "next_tok", OriginProjectID: alpha.ID,
	})))
	if len(originPrev.Files) != 1 || originPrev.Files[0].Path != "a.go" {
		t.Fatalf("origin replace preview = %+v", originPrev)
	}
	if got := spy.listCalls.Load(); got != 0 {
		t.Fatalf("origin-scoped replace preview called List() %d times, want 0", got)
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
