package sourcecontracts

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/watchfd"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestMakeProjectSourceEditableRoute(t *testing.T) {
	srv := contractfixture.NewTestServer(t)
	root := t.TempDir()
	path := filepath.Join(root, "locked.txt")
	testutil.FailErr(t, "write fixture", os.WriteFile(path, []byte("locked\n"), 0o444))
	p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, root)
	testutil.FailErr(t, "create project", err)
	read, err := project.ReadProjectSource(p, project.SourceReadRequest{RootID: p.Roots[0].ID, Path: "locked.txt"})
	testutil.FailErr(t, "read source", err)

	body, err := json.Marshal(wire.MakeProjectSourceEditableRequest{
		Path: "locked.txt", RootID: p.Roots[0].ID, BaseSHA256: read.SHA256,
	})
	testutil.FailErr(t, "encode request", err)
	req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/projects/"+p.ID+"/source/editable", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var response wire.MakeProjectSourceEditableResponse
	testutil.FailErr(t, "decode response", json.Unmarshal(w.Body.Bytes(), &response))
	if !response.Writable || response.Path != "locked.txt" || response.RootID != p.Roots[0].ID {
		t.Fatalf("response = %+v", response)
	}
	info, err := os.Stat(path)
	testutil.FailErr(t, "stat result", err)
	if info.Mode().Perm()&0o200 == 0 {
		t.Fatalf("mode = %o, want owner-write", info.Mode().Perm())
	}
}

func TestProjectSourceMutationRejectsLifecycleTransition(t *testing.T) {
	gate := project.NewMutationGate()
	srv := contractfixture.NewTestServer(t, func(d *hostapi.Dependencies) {
		d.Core.MutationGate = gate
		if d.Core.Sessions != nil {
			d.Core.Sessions.SetMutationGate(gate)
		}
	})
	root := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, root)
	testutil.FailErr(t, "create project", err)
	testutil.FailErr(t, "begin lifecycle transition", gate.BeginMutation(p.ID))
	t.Cleanup(func() { gate.EndMutation(p.ID) })

	req := contractfixture.NewAuthedRequest(
		http.MethodPost,
		"/v1/projects/"+p.ID+"/source",
		strings.NewReader(`{}`),
	)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var response wire.ErrorResponse
	testutil.FailErr(t, "decode response", json.Unmarshal(w.Body.Bytes(), &response))
	if response.Code != wire.ApiErrorCodeProjectMutationInProgress {
		t.Fatalf("code = %q want %q", response.Code, wire.ApiErrorCodeProjectMutationInProgress)
	}
}

func TestWorkerBranchRoot(t *testing.T) {
	t.Parallel()
	const projectID = "11111111-1111-4111-8111-111111111111"
	branch := t.TempDir()

	contractfixture.CompleteBranchMeta(t, branch)
	q := worker.NewInMemoryQueue(1)
	workerID := contractfixture.EnqueueBranchJob(t, q, projectID, branch)
	otherJobID := contractfixture.EnqueueBranchJob(t, q, "22222222-2222-4222-8222-222222222222", t.TempDir())

	sources := contractfixture.NewTestSources(t, q)

	cases := []struct {
		name     string
		workerID string
		want     string
	}{
		{"no worker_id reads project roots", "", ""},
		{"live overlay is searched", workerID, branch},
		{"unknown job contributes no overlay", "33333333-3333-4333-8333-333333333333", ""},
		{"job from another project is ignored", otherJobID, ""},
		{"blank worker_id reads project roots", " ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, release := sources.Workspace.WorkerBranchRoot(context.Background(), projectID, tc.workerID)
			defer release()
			if got != tc.want {
				t.Fatalf("branch root = %q want %q", got, tc.want)
			}
		})
	}
}

// newTestSources builds the source routes alone over workers and the required
// test services.

func TestWorkerBranchRootEmptyAfterPromoteReleasesOverlay(t *testing.T) {
	t.Parallel()
	const projectID = "44444444-4444-4444-8444-444444444444"

	q := worker.NewInMemoryQueue(1)
	workerID := contractfixture.EnqueueBranchJob(t, q, projectID, "")

	sources := contractfixture.NewTestSources(t, q)
	if got, release := sources.Workspace.WorkerBranchRoot(context.Background(), projectID, workerID); got != "" {
		release()
		t.Fatalf("branch root = %q want empty", got)
	}
}

func TestWorkerBranchRootEmptyForUnknownWorker(t *testing.T) {
	t.Parallel()
	sources := contractfixture.NewTestSources(t, worker.NewInMemoryQueue(1))
	if got, release := sources.Workspace.WorkerBranchRoot(context.Background(), "p", "55555555-5555-4555-8555-555555555555"); got != "" {
		release()
		t.Fatalf("branch root = %q want empty", got)
	}
}

func TestProjectSourceIndexWarmsOffRequestPath(t *testing.T) {
	srv := contractfixture.NewTestServer(t)
	dir := t.TempDir()
	testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644))
	p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, dir)
	testutil.FailErr(t, "create project", err)

	var summary wire.SourceIndexSummary
	ready := testutil.WaitForNoFatal(time.Second, func() bool {
		req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source/index", nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code == http.StatusOK {
			testutil.FailErr(t, "decode source index", json.Unmarshal(w.Body.Bytes(), &summary))
			return summary.State == wire.SourceIndexStateReady && !summary.Refreshing
		}
		if w.Code != http.StatusAccepted {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
		return false
	})
	if !ready || summary.FileCount != 1 || summary.Revision == 0 {
		t.Fatalf("summary = %+v", summary)
	}
	if len(summary.Coverage) != 1 || summary.Coverage[0].RootID != p.Roots[0].ID ||
		!summary.Coverage[0].DiscoveryComplete || summary.Coverage[0].Refreshing || summary.Coverage[0].Error != "" {
		t.Fatalf("settled summary coverage = %+v", summary.Coverage)
	}
}

func TestSourceSearchRestrictsCoverageToRequestedRoot(t *testing.T) {
	srv := contractfixture.NewTestServer(t)
	dir := t.TempDir()
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(dir, "match.txt"), []byte("source"), 0600))
	p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, dir)
	testutil.FailErr(t, "create project", err)
	other := t.TempDir()
	_, err = srv.Sources.Workspace.ProjectRegistry.AttachRoot(t.Context(), p.ID, project.AttachRootParams{Path: other})
	testutil.FailErr(t, "attach unavailable root", err)
	testutil.FailErr(t, "remove unavailable root", os.Remove(other))
	var result wire.SourceSearchResponse
	ready := testutil.WaitForNoFatal(5*time.Second, func() bool {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source/search?q=match&root_id="+p.Roots[0].ID, nil))
		if w.Code != http.StatusOK && w.Code != http.StatusAccepted {
			t.Fatalf("source search=%d %s", w.Code, w.Body.String())
		}
		testutil.FailErr(t, "decode scoped search", json.Unmarshal(w.Body.Bytes(), &result))
		return result.State == wire.SourceIndexStateReady && !result.Refreshing
	})
	if !ready || len(result.Matches) != 1 || len(result.Coverage) != 1 || result.Coverage[0].RootID != p.Roots[0].ID || result.Coverage[0].Error != "" {
		t.Fatalf("scoped search includes unavailable root: %+v", result)
	}
}

func TestSourceSearchReadsAPastedFullPathAndLocation(t *testing.T) {
	srv := contractfixture.NewTestServer(t)
	dir := t.TempDir()
	testutil.FailErr(t, "create directory", os.MkdirAll(filepath.Join(dir, "docs"), 0o700))
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(dir, "docs", "release.md"), []byte("source"), 0o600))
	p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, dir)
	testutil.FailErr(t, "create project", err)
	search := func(q string) (wire.SourceSearchResponse, string) {
		var result wire.SourceSearchResponse
		var body string
		ready := testutil.WaitForNoFatal(5*time.Second, func() bool {
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source/search?q="+url.QueryEscape(q), nil))
			if w.Code != http.StatusOK && w.Code != http.StatusAccepted {
				t.Fatalf("source search=%d %s", w.Code, w.Body.String())
			}
			body = w.Body.String()
			testutil.FailErr(t, "decode search", json.Unmarshal(w.Body.Bytes(), &result))
			return result.State == wire.SourceIndexStateReady && !result.Refreshing
		})
		if !ready {
			t.Fatalf("source index never settled: %s", body)
		}
		return result, body
	}

	pasted := "`" + filepath.Join(dir, "docs", "release.md") + ":12:3`"
	result, _ := search(pasted)
	want := []wire.SourceSearchMatch{{RootID: p.Roots[0].ID, Path: "docs/release.md", Highlights: []wire.SourceSearchHighlight{{Start: 0, End: 15}}}}
	if !reflect.DeepEqual(result.Matches, want) {
		t.Fatalf("matches = %+v, want %+v", result.Matches, want)
	}
	if result.Location == nil || *result.Location != (wire.SourceSearchLocation{Line: 12, Column: 3}) {
		t.Fatalf("location = %+v, want 12:3", result.Location)
	}

	result, body := search("")
	if len(result.Matches) != 1 || result.Location != nil || result.Outside != nil || !strings.Contains(body, `"highlights":[]`) {
		t.Fatalf("browse = %s", body)
	}

	elsewhere := t.TempDir()
	notes := filepath.Join(elsewhere, "notes.md")
	testutil.FailErr(t, "write outside", os.WriteFile(notes, []byte("notes"), 0o600))
	result, body = search(notes + "#L3-L9")
	if result.Outside == nil || *result.Outside != (wire.SourceSearchOutside{Path: notes, Kind: "file"}) {
		t.Fatalf("outside = %s", body)
	}
	if result.Location == nil || *result.Location != (wire.SourceSearchLocation{Line: 3, EndLine: 9}) {
		t.Fatalf("range = %s", body)
	}
}

func TestProjectSourceReadCarriesWorkspaceIdentity(t *testing.T) {
	srv := contractfixture.NewTestServer(t, contractfixture.WithSQLProjects(t))
	dir := t.TempDir()
	testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644))
	p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, dir)
	testutil.FailErr(t, "create project", err)

	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source?path=a.txt", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var response wire.ProjectSourceReadResponse
	testutil.FailErr(t, "decode response", json.Unmarshal(w.Body.Bytes(), &response))
	if response.WorkspaceID != p.WorkspaceID() || response.WorkspaceKind != wire.SourceWorkspaceKindProject {
		t.Fatalf("workspace = %q/%q, want %q/%q", response.WorkspaceID, response.WorkspaceKind, p.WorkspaceID(), wire.SourceWorkspaceKindProject)
	}
}

func TestSourceWorkspaceCarriesResolvedRoots(t *testing.T) {
	srv := contractfixture.NewTestServer(t)
	dir := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, dir)
	testutil.FailErr(t, "create project", err)

	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source/workspace", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var response wire.SourceWorkspace
	testutil.FailErr(t, "decode response", json.Unmarshal(w.Body.Bytes(), &response))
	if response.WorkspaceID != p.WorkspaceID() {
		t.Fatalf("workspace_id = %q, want %q", response.WorkspaceID, p.WorkspaceID())
	}
	if len(response.Roots) != 1 || response.Roots[0].ID != p.Roots[0].ID || filepath.Clean(response.Roots[0].Path) != filepath.Clean(p.Roots[0].Path) {
		t.Fatalf("roots = %#v, want %q at %q", response.Roots, p.Roots[0].ID, p.Roots[0].Path)
	}
}

func TestSourceWorkspaceReportsWatchCoverage(t *testing.T) {
	srv := contractfixture.NewTestServer(t)
	dir := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, dir)
	testutil.FailErr(t, "create project", err)
	// Bind by the path the project records, as the server does; the registry
	// keys watchers by that string.
	sourcefeed.EnsureProjectWatch(t.Context(), p.ID, "", []sourcefeed.RootSpec{{ID: p.Roots[0].ID, WorkspaceID: p.WorkspaceID(), Path: p.Roots[0].Path}}, nil)
	t.Cleanup(func() { sourcefeed.StopProjectWatch(t.Context(), p.ID) })

	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source/workspace", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var response wire.SourceWorkspace
	testutil.FailErr(t, "decode response", json.Unmarshal(w.Body.Bytes(), &response))
	watch := response.Roots[0].Watch
	if watch.State != wire.SourceWatchLive && watch.State != wire.SourceWatchPartial {
		t.Fatalf("watch = %+v, want a bound state", watch)
	}
	if watch.Recursive != watchfd.Recursive {
		t.Fatalf("recursive = %v, want the platform's %v", watch.Recursive, watchfd.Recursive)
	}
}

func TestSourceWatchCoverageDTO(t *testing.T) {
	cases := []struct {
		name     string
		coverage repochange.WatchCoverage
		want     wire.SourceWatchState
	}{
		{name: "unbound", coverage: repochange.WatchCoverage{}, want: wire.SourceWatchUnwatched},
		{name: "complete", coverage: repochange.WatchCoverage{Watching: true, Complete: true}, want: wire.SourceWatchLive},
		{name: "truncated", coverage: repochange.WatchCoverage{Watching: true, Truncated: 3}, want: wire.SourceWatchPartial},
		{name: "faulted outranks truncation", coverage: repochange.WatchCoverage{Watching: true, Truncated: 3, Faulted: true}, want: wire.SourceWatchFaulted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sourceapi.SourceWatchCoverageDTO(tc.coverage)
			if got.State != tc.want || got.UnwatchedDirectories != tc.coverage.Truncated {
				t.Fatalf("dto = %+v, want state %q", got, tc.want)
			}
		})
	}
}

func TestProjectSourceReadsRejectWorkspaceChangedDuringRead(t *testing.T) {
	tests := map[string]func(*project.Project) string{
		"browse": func(p *project.Project) string {
			return "/v1/projects/" + p.ID + "/source/browse?root_id=" + p.Roots[0].ID + "&dir=."
		},
	}
	for name, requestPath := range tests {
		t.Run(name, func(t *testing.T) {
			database := testdbfixture.Open(t, "projects.db")
			switching := &contractfixture.SwitchingProjectRegistry{Registry: project.NewSQLRegistry(database)}
			srv := contractfixture.NewTestServer(t, func(d *hostapi.Dependencies) { d.Core.Database, d.Core.Projects = database, switching })
			dir := t.TempDir()
			testutil.FailErr(t, "write text fixture", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("old"), 0o644))
			png := []byte{
				0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
				0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
			}
			testutil.FailErr(t, "write image fixture", os.WriteFile(filepath.Join(dir, "a.png"), png, 0o644))
			p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, dir)
			testutil.FailErr(t, "create project", err)

			replacement := *p
			replacement.Roots = append([]project.Root(nil), p.Roots...)
			replacement.Roots[0].Path = t.TempDir()
			if replacement.WorkspaceID() == p.WorkspaceID() {
				t.Fatal("fixture workspaces must differ")
			}
			switching.Arm(p, &replacement)

			req := contractfixture.NewAuthedRequest(http.MethodGet, requestPath(p), nil)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
			}
			var response wire.ErrorResponse
			testutil.FailErr(t, "decode response", json.Unmarshal(w.Body.Bytes(), &response))
			if response.Code != wire.ApiErrorCodeSourceWorkspaceMismatch {
				t.Fatalf("code = %q want %q", response.Code, wire.ApiErrorCodeSourceWorkspaceMismatch)
			}
			if response.Details["expected_workspace_id"] != p.WorkspaceID() ||
				response.Details["actual_workspace_id"] != replacement.WorkspaceID() {
				t.Fatalf("details = %+v", response.Details)
			}
		})
	}
}

// File reads retain their root-relative address while roots move.
// Tree listings remain pinned to their physical workspace.

func TestProjectSourceFileReadsSurviveTheWorkspaceChanging(t *testing.T) {
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	}
	for name, requestPath := range map[string]func(*project.Project) string{
		"text": func(p *project.Project) string {
			return "/v1/projects/" + p.ID + "/source?path=a.txt"
		},
		"raw": func(p *project.Project) string {
			return "/v1/projects/" + p.ID + "/source/raw?path=a.png"
		},
	} {
		t.Run(name, func(t *testing.T) {
			database := testdbfixture.Open(t, "projects.db")
			switching := &contractfixture.SwitchingProjectRegistry{Registry: project.NewSQLRegistry(database)}
			srv := contractfixture.NewTestServer(t, func(d *hostapi.Dependencies) { d.Core.Database, d.Core.Projects = database, switching })
			dir := t.TempDir()
			testutil.FailErr(t, "write text fixture", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("old"), 0o644))
			testutil.FailErr(t, "write image fixture", os.WriteFile(filepath.Join(dir, "a.png"), png, 0o644))
			p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, dir)
			testutil.FailErr(t, "create project", err)

			// The same root, re-addressed while the read is in flight.
			moved := t.TempDir()
			testutil.FailErr(t, "install moved text", os.WriteFile(filepath.Join(moved, "a.txt"), []byte("old"), 0o644))
			testutil.FailErr(t, "install moved image", os.WriteFile(filepath.Join(moved, "a.png"), png, 0o644))
			replacement := *p
			replacement.Roots = append([]project.Root(nil), p.Roots...)
			replacement.Roots[0].Path = moved
			if replacement.WorkspaceID() == p.WorkspaceID() {
				t.Fatal("fixture workspaces must differ")
			}
			switching.Arm(p, &replacement)

			req := contractfixture.NewAuthedRequest(http.MethodGet, requestPath(p), nil)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

// completeBranchMeta records a single-root layout for a branch directory, the
// way a claim would, so the branch reads as a complete worker tree.
