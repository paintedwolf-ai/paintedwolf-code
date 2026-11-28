package hostcontracts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestDetachRootRemovesDetachedWatcherAndKeepsSurvivorLive(t *testing.T) {
	sessionFixture := storetest.New(t)
	reg := project.NewSQLRegistry(sessionFixture.DB())
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{
		Core:    hostapi.CoreDependencies{Database: sessionFixture.DB(), Store: sessionFixture, Projects: reg},
		Storage: hostapi.StorageDependencies{DataDir: t.TempDir()},
	}), nil, hostapi.TestAPIToken)

	surviving, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve surviving", err)
	detached, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve detached", err)
	detachedSub := filepath.Join(detached, "nested")
	testutil.FailErr(t, "mkdir detached nested", os.MkdirAll(detachedSub, 0o755))

	p, err := reg.Create(t.Context(), project.CreateParams{
		Name: "detach-watch",
		Roots: []project.AttachRootParams{
			{Path: surviving},
			{Path: detached},
		},
	})
	testutil.FailErr(t, "create project", err)
	t.Cleanup(func() {
		_ = sourcefeed.StopProjectWatch(context.Background(), p.ID)
		repochange.CloseRoot(surviving)
		repochange.CloseRoot(detached)
	})

	var detachedID string
	for _, r := range p.Roots {
		if r.Path == detached {
			detachedID = r.ID
		}
	}
	if detachedID == "" {
		t.Fatal("detached root ID not found")
	}

	// Simulate watchers bound via both sourcefeed and direct root seeding (e.g. credential index).
	srv.Sources.Watch.EnsureSourceWatch(t.Context(), p.ID)
	repochange.SeedWatch(t.Context(), detached, []repochange.WatchDirectory{
		{Path: detached, EntryCount: 1},
		{Path: detachedSub, EntryCount: 0},
	})
	testutil.WaitFor(t, "both roots complete coverage", func() bool {
		return repochange.Coverage(surviving).Complete && repochange.Coverage(detached).Complete && repochange.DirWatched(detached, "nested")
	})

	req := contractfixture.NewAuthedRequest(http.MethodDelete, "/v1/projects/"+p.ID+"/roots/"+detachedID, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("detach status = %d body = %s", rec.Code, rec.Body.String())
	}
	contractfixture.DrainBackground(t, srv)

	if cov := repochange.Coverage(detached); cov.Watching {
		t.Fatalf("detached root still watched after HTTP 204: %+v", cov)
	}
	if repochange.DirWatched(detached, "nested") {
		t.Fatal("detached child directory still watched after HTTP 204")
	}
	if cov := repochange.Coverage(surviving); !cov.Watching || !cov.Complete {
		t.Fatalf("surviving root coverage disrupted after peer detach: %+v", cov)
	}

	wsReq := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source/workspace", nil)
	wsRec := httptest.NewRecorder()
	srv.ServeHTTP(wsRec, wsReq)
	if wsRec.Code != http.StatusOK {
		t.Fatalf("workspace status = %d body = %s", wsRec.Code, wsRec.Body.String())
	}
	var ws wire.SourceWorkspace
	testutil.FailErr(t, "decode workspace", json.NewDecoder(wsRec.Body).Decode(&ws))
	if len(ws.Roots) != 1 || ws.Roots[0].Path != surviving || ws.Roots[0].Watch.State != wire.SourceWatchLive {
		t.Fatalf("workspace roots after detach = %+v, want single live surviving root", ws.Roots)
	}
}

func TestDetachRootPreservesWatchSharedWithAnotherProject(t *testing.T) {
	sessionFixture := storetest.New(t)
	reg := project.NewSQLRegistry(sessionFixture.DB())
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{
		Core:    hostapi.CoreDependencies{Database: sessionFixture.DB(), Store: sessionFixture, Projects: reg},
		Storage: hostapi.StorageDependencies{DataDir: t.TempDir()},
	}), nil, hostapi.TestAPIToken)

	shared, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve shared", err)
	keepA, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve keepA", err)

	pA, err := reg.Create(t.Context(), project.CreateParams{
		Name:  "proj-a",
		Roots: []project.AttachRootParams{{Path: keepA}, {Path: shared}},
	})
	testutil.FailErr(t, "create projA", err)
	pB, err := reg.Create(t.Context(), project.CreateParams{
		Name:  "proj-b",
		Roots: []project.AttachRootParams{{Path: shared}},
	})
	testutil.FailErr(t, "create projB", err)
	t.Cleanup(func() {
		_ = sourcefeed.StopProjectWatch(context.Background(), pA.ID)
		_ = sourcefeed.StopProjectWatch(context.Background(), pB.ID)
		repochange.CloseRoot(shared)
		repochange.CloseRoot(keepA)
	})

	srv.Sources.Watch.EnsureSourceWatch(t.Context(), pA.ID)
	srv.Sources.Watch.EnsureSourceWatch(t.Context(), pB.ID)

	var sharedRootID string
	for _, r := range pA.Roots {
		if r.Path == shared {
			sharedRootID = r.ID
		}
	}
	req := contractfixture.NewAuthedRequest(http.MethodDelete, "/v1/projects/"+pA.ID+"/roots/"+sharedRootID, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("detach shared status = %d body = %s", rec.Code, rec.Body.String())
	}
	contractfixture.DrainBackground(t, srv)

	if !repochange.Coverage(shared).Watching {
		t.Fatal("shared root watcher closed while another project still attached it")
	}
}

func TestWorkspaceReportsFaultedOnRootOutageAndRecoversOnRebind(t *testing.T) {
	sessionFixture := storetest.New(t)
	reg := project.NewSQLRegistry(sessionFixture.DB())
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{
		Core:    hostapi.CoreDependencies{Database: sessionFixture.DB(), Store: sessionFixture, Projects: reg},
		Storage: hostapi.StorageDependencies{DataDir: t.TempDir()},
	}), nil, hostapi.TestAPIToken)

	parent, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve parent", err)
	mounted := filepath.Join(parent, "vol-root")
	testutil.FailErr(t, "mkdir mounted", os.MkdirAll(mounted, 0o755))

	p, err := reg.Create(t.Context(), project.CreateParams{
		Name:  "outage-watch",
		Roots: []project.AttachRootParams{{Path: mounted}},
	})
	testutil.FailErr(t, "create project", err)
	t.Cleanup(func() {
		_ = sourcefeed.StopProjectWatch(context.Background(), p.ID)
		repochange.CloseRoot(mounted)
	})

	var mu sync.Mutex
	var observed []string
	unbind := repochange.RegisterObserver(func(_ context.Context, event repochange.Event) {
		if event.ProjectDir == mounted && event.Kind == repochange.WorktreeChanged && event.Source == repochange.SourceWatcher {
			mu.Lock()
			observed = append(observed, event.Paths...)
			mu.Unlock()
		}
	})
	t.Cleanup(unbind)

	srv.Sources.Watch.EnsureSourceWatch(t.Context(), p.ID)
	testutil.WaitFor(t, "initial watch live", func() bool {
		return repochange.Coverage(mounted).Complete
	})

	getWorkspaceState := func() wire.SourceWatchState {
		req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/source/workspace", strings.NewReader(""))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("workspace status = %d body = %s", rec.Code, rec.Body.String())
		}
		var ws wire.SourceWorkspace
		testutil.FailErr(t, "decode workspace", json.NewDecoder(rec.Body).Decode(&ws))
		if len(ws.Roots) != 1 {
			t.Fatalf("workspace roots len = %d, want 1", len(ws.Roots))
		}
		return ws.Roots[0].Watch.State
	}

	if state := getWorkspaceState(); state != wire.SourceWatchLive {
		t.Fatalf("initial watch.state = %q, want live", state)
	}

	// Simulate volume unmount outage.
	testutil.FailErr(t, "remove mounted root", os.RemoveAll(mounted))
	if state := getWorkspaceState(); state != wire.SourceWatchFaulted {
		t.Fatalf("outage watch.state = %q, want faulted", state)
	}

	// Simulate remount and SSE/watch rebind boundary.
	testutil.FailErr(t, "recreate mounted root", os.MkdirAll(mounted, 0o755))
	srv.Sources.Watch.EnsureSourceWatch(t.Context(), p.ID)
	testutil.WaitFor(t, "recovered watch live", func() bool {
		return getWorkspaceState() == wire.SourceWatchLive && repochange.Coverage(mounted).Complete
	})

	mu.Lock()
	observed = nil
	mu.Unlock()
	testutil.FailErr(t, "write after remount", os.WriteFile(filepath.Join(mounted, "remounted.txt"), []byte("ok"), 0o644))
	testutil.WaitFor(t, "post-remount write delivered", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.Contains(observed, "remounted.txt")
	})
}
