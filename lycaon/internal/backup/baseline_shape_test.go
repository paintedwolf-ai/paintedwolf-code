package backup_test

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testutil"
)

// The extra index changes the shape without changing the revision marker.
func moveStoreShape(t *testing.T, path string) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", "file:"+path)
	testutil.FailErr(t, "open store for shape change", err)
	defer func() { _ = sqlDB.Close() }()
	_, err = sqlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_store_meta_shape_probe ON store_meta(value)`)
	testutil.FailErr(t, "add index", err)
}

func TestStageRefusesAnArchiveWhoseStoreShapeMoved(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "store.db")
	store, err := db.Open(dbPath)
	testutil.FailErr(t, "open store", err)
	testutil.FailErr(t, "close store", store.Shutdown(ctx))
	moveStoreShape(t, dbPath)

	reader, err := db.OpenReadOnly(ctx, dbPath)
	testutil.FailErr(t, "reopen store", err)
	t.Cleanup(func() { _ = reader.Close() })

	raw, _, err := createTestArchive(t, ctx, backup.CreateOpts{
		ConfigDir: configDir, SQLDB: reader, DBPath: dbPath,
		AppVersion: "test", SchemaUserVersion: db.SchemaVersion, Now: time.Now().UTC(),
	})
	testutil.FailErr(t, "create archive", err)

	_, err = backup.Stage(ctx, backup.StageOpts{
		ConfigDir: configDir, ArchivePath: writeTempArchive(t, raw),
		SQLDB: reader, SchemaVersion: db.SchemaVersion,
	})
	if !errors.Is(err, backup.ErrBaselineMismatch) {
		t.Fatalf("stage err = %v, want ErrBaselineMismatch for an archive whose store structure moved", err)
	}
	if _, statErr := os.Stat(backup.PendingMarkerPath(configDir)); !os.IsNotExist(statErr) {
		t.Fatal("a shape-mismatched archive published a pending marker")
	}
}

func TestStageChecksTheStagedStoreNotOnlyTheManifestClaim(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "store.db")
	store, err := db.Open(dbPath)
	testutil.FailErr(t, "open store", err)
	testutil.FailErr(t, "close store", store.Shutdown(ctx))

	baselineDigest, err := db.BaselineShapeDigest(ctx)
	testutil.FailErr(t, "baseline digest", err)

	moveStoreShape(t, dbPath)
	reader, err := db.OpenReadOnly(ctx, dbPath)
	testutil.FailErr(t, "reopen store", err)
	t.Cleanup(func() { _ = reader.Close() })

	raw, _, err := createTestArchive(t, ctx, backup.CreateOpts{
		ConfigDir: configDir, SQLDB: reader, DBPath: dbPath,
		AppVersion: "test", SchemaUserVersion: db.SchemaVersion, Now: time.Now().UTC(),
	})
	testutil.FailErr(t, "create archive", err)
	// Claim the current baseline while shipping the moved store.
	forged := rewriteTestManifest(t, raw, func(m *backup.Manifest) {
		m.SchemaShapeDigest = baselineDigest
	})

	_, err = backup.Stage(ctx, backup.StageOpts{
		ConfigDir: configDir, ArchivePath: writeTempArchive(t, forged),
		SQLDB: reader, SchemaVersion: db.SchemaVersion,
	})
	if !errors.Is(err, backup.ErrBaselineMismatch) {
		t.Fatalf("stage err = %v, want ErrBaselineMismatch from the staged store itself", err)
	}
}

func createTestArchive(t *testing.T, ctx context.Context, opts backup.CreateOpts) ([]byte, backup.Manifest, error) {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "archive.zip")
	manifest, err := backup.Create(ctx, opts, dest)
	if err != nil {
		return nil, backup.Manifest{}, err
	}
	raw, readErr := os.ReadFile(dest) // #nosec G304 -- test-owned path
	testutil.FailErr(t, "read archive", readErr)
	return raw, manifest, nil
}

func writeTempArchive(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "staged.zip")
	testutil.FailErr(t, "write archive", os.WriteFile(path, raw, 0o600))
	return path
}

func rewriteTestManifest(t *testing.T, raw []byte, mutate func(*backup.Manifest)) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	testutil.FailErr(t, "read zip", err)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		rc, openErr := f.Open()
		testutil.FailErr(t, "open entry", openErr)
		body, readErr := io.ReadAll(rc)
		_ = rc.Close()
		testutil.FailErr(t, "read entry", readErr)
		if f.Name == "manifest.json" {
			var m backup.Manifest
			testutil.FailErr(t, "unmarshal manifest", json.Unmarshal(body, &m))
			mutate(&m)
			body, err = json.MarshalIndent(m, "", "  ")
			testutil.FailErr(t, "marshal manifest", err)
		}
		w, createErr := zw.Create(f.Name)
		testutil.FailErr(t, "create entry", createErr)
		_, writeErr := w.Write(body)
		testutil.FailErr(t, "write entry", writeErr)
	}
	testutil.FailErr(t, "close zip", zw.Close())
	return buf.Bytes()
}
