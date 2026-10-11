//go:build integration

package searchcontracts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	catalogtest "github.com/lycaon/lycaon/internal/testsetup/sourcecatalog"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	hostapi "github.com/lycaon/lycaon/internal/api"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestHandleSearchGlobalAndOriginRanking(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "store.db")

	dirA := t.TempDir()
	dirB := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, "proj-a", dirA)
	testdbseed.InsertProjectRoot(t, sqlDB, "proj-b", dirB)
	contractfixture.SeedSearchRow(t, sqlDB, search.IndexRow{
		ID: "a1", ProjectID: "proj-a", Source: search.SourceTool, HitKind: search.HitKindWeb,
		Snippet: "api-shared-token", TS: contractfixture.FormatSearchTS(time.Now().UTC()),
	})
	contractfixture.SeedSearchRow(t, sqlDB, search.IndexRow{
		ID: "b1", ProjectID: "proj-b", Source: search.SourceTool, HitKind: search.HitKindWeb,
		Snippet: "api-shared-token", TS: contractfixture.FormatSearchTS(time.Now().UTC()),
	})

	st := store.NewSQL(sqlDB)
	reg := project.NewSQLRegistry(sqlDB)
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: st, Projects: reg}}), nil, hostapi.TestAPIToken)

	body := `{"query":"api-shared-token","origin_project_id":"proj-b"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/search", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp wire.SearchResponse
	testutil.FailErr(t, "decode", json.Unmarshal(rec.Body.Bytes(), &resp))
	if len(resp.Hits) < 2 {
		t.Fatalf("hits = %d", len(resp.Hits))
	}
	if resp.Hits[0].ProjectID != "proj-b" {
		t.Fatalf("first hit project = %q", resp.Hits[0].ProjectID)
	}
}

func TestHandleSearchReplaceApplyExactReplay(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "store.db")
	rootPath := t.TempDir()
	rootID := testdbseed.InsertProjectRoot(t, sqlDB, "proj-replace", rootPath)
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(rootPath, "a.txt"), []byte("old value\n"), 0o644))
	p, err := project.NewSQLRegistry(sqlDB).Get(t.Context(), "proj-replace")
	testutil.FailErr(t, "get project", err)
	read, err := projectsource.ReadProjectSource(p, projectsource.SourceReadRequest{RootID: rootID, Path: "a.txt"})
	testutil.FailErr(t, "read source", err)

	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: store.NewSQL(sqlDB), Projects: project.NewSQLRegistry(sqlDB)}}), nil, hostapi.TestAPIToken)
	opID := uuid.NewString()
	request := wire.SearchReplaceApplyRequest{OperationID: opID, Query: "old", Replacement: "new",
		OriginProjectID: p.ID, Files: []wire.SearchReplaceApplyFile{{RootID: rootID, Path: "a.txt", SHA256: read.SHA256, Hunks: []int{0}}}}
	call := func(body wire.SearchReplaceApplyRequest) *httptest.ResponseRecorder {
		raw, marshalErr := json.Marshal(body)
		testutil.FailErr(t, "marshal request", marshalErr)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, contractfixture.NewAuthedRequest(http.MethodPost, "/v1/search/replace/apply", strings.NewReader(string(raw))))
		return rec
	}
	first := call(request)
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	replayed := call(request)
	if replayed.Code != http.StatusOK || replayed.Body.String() != first.Body.String() {
		t.Fatalf("replay status=%d body=%s first=%s", replayed.Code, replayed.Body.String(), first.Body.String())
	}
	content, err := os.ReadFile(filepath.Join(rootPath, "a.txt"))
	testutil.FailErr(t, "read replaced source", err)
	if string(content) != "new value\n" {
		t.Fatalf("source=%q", content)
	}
	request.Replacement = "different"
	conflict := call(request)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "idempotency_conflict") {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
}

func TestHandleSearchFacetValuesNeverNull(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "store.db")

	dirA := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, "proj-a", dirA)
	contractfixture.SeedSearchRow(t, sqlDB, search.IndexRow{
		ID: "a1", ProjectID: "proj-a", Source: search.SourceTool, HitKind: search.HitKindWeb,
		Snippet: "facet-null-token", TS: contractfixture.FormatSearchTS(time.Now().UTC()),
	})

	st := store.NewSQL(sqlDB)
	reg := project.NewSQLRegistry(sqlDB)
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: st, Projects: reg}}), nil, hostapi.TestAPIToken)

	body := `{"query":"facet-null-token","origin_project_id":"proj-a"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/search", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	// Empty facet values serialize as arrays.
	if strings.Contains(rec.Body.String(), `"values":null`) {
		t.Fatalf("facet values serialized as null: %s", rec.Body.String())
	}

	var resp wire.SearchResponse
	testutil.FailErr(t, "decode", json.Unmarshal(rec.Body.Bytes(), &resp))
	for _, facet := range resp.Facets {
		if facet.Values == nil {
			t.Fatalf("facet %q has nil values", facet.Key)
		}
	}
}

func TestHandleSearchResolvesWorkerContext(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "store.db")

	dirA := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, "proj-a", dirA)
	testdbseed.InsertSession(t, sqlDB, "parent-sess", "proj-a")
	testdbseed.InsertSession(t, sqlDB, "child-sess", "proj-a")
	_, err := sqlDB.ExecContext(context.Background(), `
		INSERT INTO worker_jobs (
			id, project_id, workspace_path, agent_type, status, prompt, brief, created_at,
			parent_session_id, child_session_id
		) VALUES ('worker-1', 'proj-a', ?, 'implementer', 'complete', 'fixture', 'fixture', ?, 'parent-sess', 'child-sess')
	`, dirA, contractfixture.FormatSearchTS(time.Now().UTC()))
	testutil.FailErr(t, "insert worker job", err)

	contractfixture.SeedSearchRow(t, sqlDB, search.IndexRow{
		ID: "w1", ProjectID: "proj-a", Source: search.SourceMessage, HitKind: search.HitKindEvidence,
		SessionID: "child-sess", Snippet: "worker-evidence-token", TS: contractfixture.FormatSearchTS(time.Now().UTC()),
	})

	st := store.NewSQL(sqlDB)
	reg := project.NewSQLRegistry(sqlDB)
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: st, Projects: reg}}), nil, hostapi.TestAPIToken)

	body := `{"query":"worker-evidence-token","origin_project_id":"proj-a"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/search", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp wire.SearchResponse
	testutil.FailErr(t, "decode", json.Unmarshal(rec.Body.Bytes(), &resp))
	if len(resp.Hits) == 0 {
		t.Fatalf("no hits: %s", rec.Body.String())
	}
	hit := resp.Hits[0]
	if hit.ParentSessionID != "parent-sess" {
		t.Fatalf("parent_session_id = %q, want parent-sess", hit.ParentSessionID)
	}
	if hit.WorkerID != "worker-1" {
		t.Fatalf("worker_id = %q, want worker-1", hit.WorkerID)
	}
}

func TestHandleSearchProjectCurrentAloneListsScopedEvidence(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "store.db")

	dirA := t.TempDir()
	dirB := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, "proj-a", dirA)
	testdbseed.InsertProjectRoot(t, sqlDB, "proj-b", dirB)
	contractfixture.SeedSearchRow(t, sqlDB, search.IndexRow{
		ID: "a1", ProjectID: "proj-a", Source: search.SourceTool, HitKind: search.HitKindWeb,
		Snippet: "scoped-only-token", TS: contractfixture.FormatSearchTS(time.Now().UTC()),
	})
	contractfixture.SeedSearchRow(t, sqlDB, search.IndexRow{
		ID: "b1", ProjectID: "proj-b", Source: search.SourceTool, HitKind: search.HitKindWeb,
		Snippet: "scoped-only-token", TS: contractfixture.FormatSearchTS(time.Now().UTC()),
	})

	st := store.NewSQL(sqlDB)
	reg := project.NewSQLRegistry(sqlDB)
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: st, Projects: reg}}), nil, hostapi.TestAPIToken)

	body := `{"query":"project:current","origin_project_id":"proj-a"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/search", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp wire.SearchResponse
	testutil.FailErr(t, "decode", json.Unmarshal(rec.Body.Bytes(), &resp))
	if len(resp.Hits) != 1 {
		t.Fatalf("hits = %d, want 1 scoped row", len(resp.Hits))
	}
	if resp.Hits[0].ProjectID != "proj-a" {
		t.Fatalf("hit project = %q", resp.Hits[0].ProjectID)
	}
}

func TestHandleSearchProjectCurrentNarrows(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "store.db")

	dirA := t.TempDir()
	dirB := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, "proj-a", dirA)
	testdbseed.InsertProjectRoot(t, sqlDB, "proj-b", dirB)
	contractfixture.SeedSearchRow(t, sqlDB, search.IndexRow{
		ID: "a1", ProjectID: "proj-a", Source: search.SourceTool, HitKind: search.HitKindWeb,
		Snippet: "api-narrow-token", TS: contractfixture.FormatSearchTS(time.Now().UTC()),
	})
	contractfixture.SeedSearchRow(t, sqlDB, search.IndexRow{
		ID: "b1", ProjectID: "proj-b", Source: search.SourceTool, HitKind: search.HitKindWeb,
		Snippet: "api-narrow-token", TS: contractfixture.FormatSearchTS(time.Now().UTC()),
	})

	st := store.NewSQL(sqlDB)
	reg := project.NewSQLRegistry(sqlDB)
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: st, Projects: reg}}), nil, hostapi.TestAPIToken)

	body := `{"query":"project:current api-narrow-token","origin_project_id":"proj-a"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/search", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp wire.SearchResponse
	testutil.FailErr(t, "decode", json.Unmarshal(rec.Body.Bytes(), &resp))
	for _, hit := range resp.Hits {
		if hit.ProjectID != "proj-a" {
			t.Fatalf("hit project = %q", hit.ProjectID)
		}
	}
}

func TestHandleSearchKindCodeFansOut(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "store.db")

	dir := t.TempDir()
	rootID := testdbseed.InsertProjectRoot(t, sqlDB, "proj-a", dir)
	if err := os.WriteFile(filepath.Join(dir, "needle.go"), []byte("package main\nvar NeedleToken = 1\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	st := store.NewSQL(sqlDB)
	reg := project.NewSQLRegistry(sqlDB)
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: st, Projects: reg}}), nil, hostapi.TestAPIToken)
	// A root still warming answers with no code hits, so settle it first.
	testutil.FailErr(t, "settle source inventory", catalogtest.AwaitIndex(t.Context(), sourcecatalog.Process(), "proj-a",
		sourcecatalog.Root{ID: rootID, Path: dir}))

	body := `{"query":"kind:code NeedleToken","origin_project_id":"proj-a"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/search", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp wire.SearchResponse
	testutil.FailErr(t, "decode", json.Unmarshal(rec.Body.Bytes(), &resp))
	for _, hit := range resp.Hits {
		if hit.HitKind == search.HitKindCode {
			return
		}
	}
	t.Fatalf("expected code hit, got %v", resp.Hits)
}

func TestHandleSearchInvalidQuery(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "store.db")

	st := store.NewSQL(sqlDB)
	reg := project.NewSQLRegistry(sqlDB)
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: st, Projects: reg}}), nil, hostapi.TestAPIToken)

	body := `{"query":"kind:"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/search", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp wire.ErrorResponse
	testutil.FailErr(t, "decode", json.Unmarshal(rec.Body.Bytes(), &resp))
	if resp.Code != "search_query_invalid" {
		t.Fatalf("code = %q", resp.Code)
	}
}

func TestHandleSearchLargeStoreLatency(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "store.db")

	const rowCount = 200
	dir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, "proj-perf", dir)
	now := time.Now().UTC()
	for i := range rowCount {
		contractfixture.SeedSearchRow(t, sqlDB, search.IndexRow{
			ID:        fmt.Sprintf("perf-%d", i),
			ProjectID: "proj-perf",
			Source:    search.SourceTool,
			HitKind:   search.HitKindWeb,
			Snippet:   fmt.Sprintf("perf-shared-token-%d", i%17),
			TS:        contractfixture.FormatSearchTS(now.Add(-time.Duration(i) * time.Minute)),
		})
	}

	st := store.NewSQL(sqlDB)
	reg := project.NewSQLRegistry(sqlDB)
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: st, Projects: reg}}), nil, hostapi.TestAPIToken)

	body := `{"query":"perf-shared-token","origin_project_id":"proj-perf"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/search", strings.NewReader(body))
	rec := httptest.NewRecorder()
	start := time.Now()
	srv.ServeHTTP(rec, req)
	elapsed := time.Since(start)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if elapsed > 3*time.Second {
		t.Fatalf("search latency = %v for %d rows, want < 3s", elapsed, rowCount)
	}
	var resp wire.SearchResponse
	testutil.FailErr(t, "decode", json.Unmarshal(rec.Body.Bytes(), &resp))
	if len(resp.Hits) == 0 {
		t.Fatal("expected hits from large-store probe")
	}
}

func TestHandleSearchRejectsUnknownOriginProject(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "store.db")
	st := store.NewSQL(sqlDB)
	reg := project.NewSQLRegistry(sqlDB)
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: st, Projects: reg}}), nil, hostapi.TestAPIToken)

	body := `{"query":"needle project:current","origin_project_id":"00000000-0000-0000-0000-000000000000"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/search", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	testutil.FailErr(t, "decode", json.Unmarshal(rec.Body.Bytes(), &resp))
	if resp["code"] != "project_not_found" {
		t.Fatalf("code = %v", resp["code"])
	}
}

func TestHandleSearchQueryErrorCarriesDetails(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "store.db")
	st := store.NewSQL(sqlDB)
	reg := project.NewSQLRegistry(sqlDB)
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: st, Projects: reg}}), nil, hostapi.TestAPIToken)

	body := `{"query":"needle kind:bogus"}`
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/search", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	}
	testutil.FailErr(t, "decode", json.Unmarshal(rec.Body.Bytes(), &resp))
	if resp.Code != "search_query_invalid" {
		t.Fatalf("code = %q", resp.Code)
	}
	// The rendered copy carries the reason, not catalog boilerplate.
	if !strings.Contains(resp.Message, "bogus") {
		t.Fatalf("error copy lost the reason: %q", resp.Message)
	}
	if resp.Details["field"] != "kind" {
		t.Fatalf("details = %+v", resp.Details)
	}
	if _, ok := resp.Details["offset"]; !ok {
		t.Fatalf("details missing offset: %+v", resp.Details)
	}
}
