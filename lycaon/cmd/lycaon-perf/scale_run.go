package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/scalefixture"
	"github.com/lycaon/lycaon/pkg/api"
)

// scaleFixtureRun builds and measures one size. Each step starts the engine
// on the store the previous step left and stops it before the next.
type scaleFixtureRun struct {
	cfg       scaleConfig
	size      scaleSize
	dir       string
	projectID string
	sessionID string
	measured  scaleMeasurement
}

func (f *scaleFixtureRun) projectDir() string { return filepath.Join(f.dir, "project") }
func (f *scaleFixtureRun) configDir() string  { return filepath.Join(f.dir, "config") }
func (f *scaleFixtureRun) storePath() string  { return filepath.Join(f.configDir(), "store.db") }

func (f *scaleFixtureRun) writeTree(context.Context) error {
	shape, err := scalefixture.WriteTree(f.projectDir(), f.size.Files)
	f.measured.Fixture.Tree = shape
	return err
}

// populate lets the engine inventory the project the way a person's first
// session does.
func (f *scaleFixtureRun) populate(ctx context.Context) (err error) {
	engine, err := startScaleEngine(ctx, f.cfg.binary, f.configDir(), filepath.Join(f.dir, "populate"))
	if err != nil {
		return err
	}
	defer func() { err = joinStop(err, engine) }()
	var project api.Project
	if err := engine.client.request(ctx, http.MethodPost, "/v1/projects", api.CreateProjectRequest{
		Roots: []api.CreateProjectRootInput{{Path: f.projectDir()}},
	}, &project); err != nil {
		return err
	}
	f.projectID = project.ID
	var session api.Session
	if err := engine.client.request(ctx, http.MethodPost, "/v1/sessions", api.CreateSessionRequest{
		ProjectID: project.ID, Posture: api.SessionPostureBuild,
	}, &session); err != nil {
		return err
	}
	f.sessionID = session.ID
	if err := engine.client.waitPrepared(ctx, session.ID); err != nil {
		return err
	}
	inventory, err := waitInventory(ctx, engine.client, project.ID)
	if err != nil {
		return err
	}
	f.measured.Fixture.IndexedFiles = int64(inventory.IndexedFiles)
	if f.measured.Fixture.IndexedFiles < int64(f.size.Files) {
		return fmt.Errorf("inventory indexed %d of %d files", inventory.IndexedFiles, f.size.Files)
	}
	return nil
}

func waitInventory(ctx context.Context, client *sidecarClient, projectID string) (api.SourceInventoryState, error) {
	for {
		var storage api.SourceStorage
		if err := client.request(ctx, http.MethodGet, "/v1/projects/"+projectID+"/source/storage", nil, &storage); err != nil {
			return api.SourceInventoryState{}, err
		}
		switch {
		case storage.Inventory.Status == "error":
			return storage.Inventory, fmt.Errorf("source inventory failed")
		case storage.Inventory.Complete && storage.Inventory.Status == "ready":
			return storage.Inventory, nil
		}
		if err := pause(ctx, time.Second); err != nil {
			return api.SourceInventoryState{}, err
		}
	}
}

func (f *scaleFixtureRun) recordHistory(ctx context.Context) error {
	return recordProjectHistory(ctx, f.configDir(), f.projectID, f.sessionID, f.size.Files)
}

// freePages grows and then drops a table the store does not otherwise hold,
// leaving exactly its pages on the freelist.
func (f *scaleFixtureRun) freePages(ctx context.Context) error {
	shape, err := addFreePages(ctx, f.storePath(), f.size.FreePages)
	f.measured.Fixture.PageSize, f.measured.Fixture.StorePages = shape.pageSize, shape.pages
	f.measured.Fixture.FreePages, f.measured.Fixture.StoreFileBytes = shape.free, shape.fileBytes
	if err == nil && shape.free < f.size.FreePages {
		err = fmt.Errorf("store has %d free pages, want at least %d", shape.free, f.size.FreePages)
	}
	return err
}

// bootAndVacuum measures boot on the full freelist, then lets the first
// maintenance drain reclaim it.
func (f *scaleFixtureRun) bootAndVacuum(ctx context.Context) (err error) {
	opens, err := watchOpens(f.projectDir())
	if err != nil {
		return err
	}
	defer func() { _ = opens.Close() }()
	engine, err := startScaleEngine(ctx, f.cfg.binary, f.configDir(), filepath.Join(f.dir, "boot"))
	if err != nil {
		return err
	}
	defer func() { err = joinStop(err, engine) }()
	boot, err := engine.capture.work(ctx, engine, "app.boot")
	if err != nil {
		return err
	}
	f.measured.Boot = scaleBoot{Work: boot, FilesOpened: opens.Count()}
	gauges, err := engine.capture.gauges(ctx, engine, func(g map[string]int64) bool {
		return g["store_maintenance_drains_done"] > 0
	})
	if err != nil {
		return err
	}
	v := &f.measured.Vacuum
	v.FreePagesBefore = f.measured.Fixture.FreePages
	v.PassPages = db.VacuumPassPages(v.FreePagesBefore, f.measured.Fixture.PageSize)
	v.Passes, v.Turns = gauges["store_maintenance_passes"], gauges["store_maintenance_turns"]
	v.VacuumPasses = gauges["store_maintenance_vacuum_passes"]
	v.PeakPassPages, v.PagesReclaimed = gauges["store_maintenance_peak_pass_pages"], gauges["store_maintenance_pages_reclaimed"]
	if err := engine.stop(); err != nil {
		return err
	}
	shape, err := readStoreShape(ctx, f.storePath())
	v.FreePagesAfter = shape.free
	return err
}

func (f *scaleFixtureRun) deleteProject(ctx context.Context) (err error) {
	owned, err := countOwnedRows(ctx, f.storePath(), f.projectID)
	if err != nil {
		return err
	}
	f.measured.Delete.OwnedRows = owned
	engine, err := startScaleEngine(ctx, f.cfg.binary, f.configDir(), filepath.Join(f.dir, "delete"))
	if err != nil {
		return err
	}
	defer func() { err = joinStop(err, engine) }()
	operation := uuid.NewString()
	var result api.ProjectRemovalResult
	if err := engine.client.request(ctx, http.MethodPost, "/v1/projects/"+f.projectID+"/removals",
		api.ProjectRemovalRequest{OperationID: operation, Force: true}, &result); err != nil {
		return err
	}
	for result.ProjectState != "deleted" {
		if result.FailureCode != "" {
			return fmt.Errorf("project removal failed: %s", result.Reason)
		}
		if err := pause(ctx, time.Second); err != nil {
			return err
		}
		if err := engine.client.request(ctx, http.MethodGet, "/v1/projects/"+f.projectID+"/removals/"+operation, nil, &result); err != nil {
			return err
		}
	}
	f.measured.Delete.Work, err = engine.capture.work(ctx, engine, "project.remove")
	return err
}

func (f *scaleFixtureRun) upgrade(ctx context.Context) (err error) {
	f.measured.Upgrade, err = measureUpgrade(ctx, f.cfg, filepath.Join(f.dir, "upgrade"), f.size.Files)
	return err
}

// measureUpgrade opens the newest released store, holding one inventory of
// entries files in its layout, with the engine under test.
func measureUpgrade(ctx context.Context, cfg scaleConfig, dir string, entries int) (_ scaleUpgrade, err error) {
	configDir := filepath.Join(dir, "config")
	if err := materializeCorpus(cfg.corpus, configDir); err != nil {
		return scaleUpgrade{}, err
	}
	store := filepath.Join(configDir, "store.db")
	if entries > 0 {
		if err := seedReleasedInventory(ctx, store, entries); err != nil {
			return scaleUpgrade{}, err
		}
	}
	shape, err := readStoreShape(ctx, store)
	if err != nil {
		return scaleUpgrade{}, err
	}
	measured := scaleUpgrade{Entries: entries, StorePages: shape.pages}
	engine, err := startScaleEngine(ctx, cfg.binary, configDir, filepath.Join(dir, "run"))
	if err != nil {
		return scaleUpgrade{}, err
	}
	defer func() { err = joinStop(err, engine) }()
	measured.Work, err = engine.capture.work(ctx, engine, "store.upgrade")
	return measured, err
}

func pause(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
