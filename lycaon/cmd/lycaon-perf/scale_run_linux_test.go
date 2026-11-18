package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// fakeEngineEnv makes the test binary serve as the engine a scale run starts.
const fakeEngineEnv = "LYCAON_PERF_FAKE_ENGINE"

func TestMain(m *testing.M) {
	if os.Getenv(fakeEngineEnv) == "1" {
		os.Exit(fakeEngine(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func TestScaleRunMeasuresEveryOperationAndReportsItsBounds(t *testing.T) {
	t.Setenv(fakeEngineEnv, "1")
	binary, err := os.Executable()
	testutil.FailErr(t, "find test binary", err)
	output := filepath.Join(t.TempDir(), "scale.json")
	var stdout bytes.Buffer
	code := scaleMain([]string{"--binary", binary, "--output", output, "--work", t.TempDir(),
		"--corpus", filepath.Join("..", "..", corpusRoot, "1.0.2"), "--divisor", "400"}, &stdout)
	if code > 1 {
		t.Fatalf("scale run exited %d:\n%s", code, stdout.String())
	}
	raw, err := os.ReadFile(output)
	testutil.FailErr(t, "read report", err)
	var report scaleReport
	testutil.FailErr(t, "decode report", json.Unmarshal(raw, &report))
	if len(report.Sizes) != 2 || report.UpgradeBase.Work["pages_fetched"] <= 0 {
		t.Fatalf("report = %+v", report)
	}
	for _, m := range report.Sizes {
		if m.Fixture.IndexedFiles != int64(m.Size.Files) || m.Fixture.FreePages < m.Size.FreePages {
			t.Fatalf("%s fixture = %+v for %+v", m.Size.Name, m.Fixture, m.Size)
		}
		if m.Boot.Work["statements"] <= 0 || m.Boot.FilesOpened < 0 {
			t.Fatalf("%s boot = %+v", m.Size.Name, m.Boot)
		}
		if m.Delete.OwnedRows < int64(m.Size.Files) || m.Delete.Work["rows_changed"] < m.Delete.OwnedRows-1 {
			t.Fatalf("%s delete = %+v", m.Size.Name, m.Delete)
		}
		if m.Vacuum.FreePagesAfter >= m.Vacuum.FreePagesBefore || m.Upgrade.Work["peak_resident_bytes"] <= 0 {
			t.Fatalf("%s vacuum %+v upgrade %+v", m.Size.Name, m.Vacuum, m.Upgrade)
		}
	}
	if !strings.Contains(stdout.String(), "Scale large") {
		t.Fatalf("summary = %s", stdout.String())
	}
}

func TestScaleRunNeedsItsEngineAndReport(t *testing.T) {
	if code := scaleMain([]string{"--output", "x"}, &bytes.Buffer{}); code != 2 {
		t.Fatalf("scale without a binary exited %d", code)
	}
	binary := filepath.Join(t.TempDir(), "missing-engine")
	if code := scaleMain([]string{"--binary", binary, "--output", filepath.Join(t.TempDir(), "r.json"), "--work", t.TempDir(),
		"--corpus", filepath.Join("..", "..", corpusRoot, "1.0.2")}, &bytes.Buffer{}); code != 1 {
		t.Fatalf("scale with a missing engine exited %d", code)
	}
}

// fakeEngine serves what a scale run asks of the engine, on a real store,
// and writes the work records the engine writes.
func fakeEngine(args []string) int {
	if len(args) != 3 || args[0] != "serve" || args[1] != "--db" {
		return 2
	}
	defer observability.CloseDebugCaptures()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	path := args[2]
	if released, err := releasedStore(path); err != nil {
		return 1
	} else if released {
		// An upgrade's own work is the engine's; its record is all a run reads.
		observability.RecordWork("store.upgrade", map[string]int64{"pages_fetched": 1_000 + fileBytes(path)/1_024, "peak_resident_bytes": 64 << 20})
		return serveFake(ctx, http.NewServeMux())
	}
	// The engine stand-in opens its store the way the engine does.
	store, err := db.OpenWithOptions(ctx, path, db.UpgradeHooks{}, db.StoreOptions{})
	if err != nil {
		return 1
	}
	defer func() { _ = store.Shutdown(context.WithoutCancel(ctx)) }()
	observability.RecordWork("app.boot", db.ProcessWork().Counters())
	vacuum(ctx, store)
	return serveFake(ctx, fakeRoutes(store))
}

func releasedStore(path string) (bool, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	database, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return false, err
	}
	defer func() { _ = database.Close() }()
	var version int
	err = database.QueryRow("PRAGMA user_version").Scan(&version)
	return version < db.SchemaVersion, err
}

func fileBytes(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// vacuum frees the freelist in bounded steps and reports them as gauges.
func vacuum(ctx context.Context, store *db.Store) {
	free, err := writerFreelist(ctx, store)
	var passes int64
	// Each pass must shrink the freelist; one that cannot ends the drain.
	for err == nil && free > 256 {
		if _, err = store.ExecContext(ctx, "PRAGMA incremental_vacuum(2048)"); err != nil {
			break
		}
		passes++
		var after int64
		if after, err = writerFreelist(ctx, store); err != nil || after >= free {
			break
		}
		free = after
	}
	observability.RecordRuntimeSample(map[string]int64{
		"store_maintenance_drains_done": 1, "store_maintenance_passes": passes, "store_maintenance_vacuum_passes": passes,
		"store_maintenance_turns": passes + 1, "store_maintenance_peak_pass_pages": min(passes, 1) * db.VacuumPassPageLimit,
		"store_maintenance_pages_reclaimed": passes * db.VacuumPassPageLimit,
	})
}

// writerFreelist reads the freelist on the connection that vacuums it.
func writerFreelist(ctx context.Context, store *db.Store) (int64, error) {
	tx, err := store.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var free int64
	err = tx.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&free)
	return free, err
}

func fakeRoutes(store *db.Store) *http.ServeMux {
	registry := project.NewSQLRegistry(store)
	routes := http.NewServeMux()
	reply := func(w http.ResponseWriter, value any) { _ = json.NewEncoder(w).Encode(value) }
	routes.HandleFunc("POST /v1/projects", func(w http.ResponseWriter, r *http.Request) {
		var request api.CreateProjectRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		created, err := registry.Create(r.Context(), project.CreateParams{Roots: []project.AttachRootParams{{Path: request.Roots[0].Path}}})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		reply(w, api.Project{ID: created.ID})
	})
	routes.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		var request api.CreateSessionRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		_, err := store.ExecContext(r.Context(), `INSERT INTO sessions (id, project_id, owner_person_id, posture, status, created_at, activity_at, updated_at)
VALUES ('fake-session', ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, request.ProjectID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		reply(w, api.Session{ID: "fake-session", Status: api.SessionStatusIdle})
	})
	routes.HandleFunc("GET /v1/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		reply(w, api.Session{ID: r.PathValue("id"), Status: api.SessionStatusIdle})
	})
	routes.HandleFunc("GET /v1/projects/{id}/source/storage", func(w http.ResponseWriter, r *http.Request) {
		p, err := registry.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		var files int64
		_ = filepath.WalkDir(p.Roots[0].Path, func(_ string, entry fs.DirEntry, err error) error {
			if err == nil && !entry.IsDir() {
				files++
			}
			return err
		})
		reply(w, api.SourceStorage{Inventory: api.SourceInventoryState{Status: "ready", Complete: true, IndexedFiles: files}})
	})
	routes.HandleFunc("POST /v1/projects/{id}/removals", func(w http.ResponseWriter, r *http.Request) {
		var work db.WorkTally
		if err := registry.Delete(db.WithWorkTally(project.WithForcedLifecycle(r.Context()), &work), r.PathValue("id")); err != nil {
			reply(w, api.ProjectRemovalResult{FailureCode: "project_removal_failed", Reason: err.Error()})
			return
		}
		observability.RecordWork("project.remove", work.Work().Counters())
		reply(w, api.ProjectRemovalResult{ProjectState: "deleted"})
	})
	return routes
}

func serveFake(ctx context.Context, routes *http.ServeMux) int {
	routes.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) })
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", os.Getenv("LYCAON_ADDR"))
	if err != nil {
		return 1
	}
	server := &http.Server{Handler: routes}
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return 1
	}
	return 0
}
