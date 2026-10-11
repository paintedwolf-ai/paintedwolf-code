package backup_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCreateArchiveIncludesVacuumedStoreAndManifest(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "store.db")
	sqlDB := testdbfixture.OpenPath(t, dbPath)

	seed := map[string]string{
		"credential-vault.age":                   "encrypted vault bytes",
		"credential-vault-identity.age":          "wrapped identity bytes",
		".credential-vault-development-identity": "development identity bytes",
		"providers.local.yaml":                   "providers: []\n",
		"model-policy.yaml":                      "policy: {}\n",
		"api.token":                              "tok\n",
		"daemon.json":                            "{}\n",
		"approvals.yaml":                         "rules: []\n",
		"limits.yaml":                            "limits: {}\n",
	}
	for rel, body := range seed {
		testutil.FailErr(t, "seed "+rel, os.WriteFile(filepath.Join(configDir, rel), []byte(body), 0o600))
	}
	appStateDir := filepath.Join(configDir, "app-state-v1")
	testutil.FailErr(t, "seed app-state dir", os.MkdirAll(appStateDir, 0o700))
	testutil.FailErr(t, "seed app-state slice",
		os.WriteFile(filepath.Join(appStateDir, "6465627567.json"), []byte(`{"key":"debug"}`), 0o600))

	now := time.Date(2026, 7, 28, 15, 0, 0, 0, time.UTC)
	raw, manifest, err := createArchive(t, ctx, backup.CreateOpts{
		ConfigDir:         configDir,
		SQLDB:             sqlDB,
		DBPath:            dbPath,
		AppVersion:        "test",
		SchemaUserVersion: db.SchemaVersion,
		Now:               now,
	})
	testutil.FailErr(t, "create", err)

	for _, rel := range localdata.CredentialRelPaths() {
		if _, ok := zipNames(t, raw)[rel]; ok {
			t.Fatalf("backup must never contain the secret store %s", rel)
		}
	}
	if manifest.FormatVersion != backup.FormatVersion {
		t.Fatalf("format_version=%d", manifest.FormatVersion)
	}
	if backup.ArchiveFilename(manifest) != "painted-wolf-backup-20260728.zip" {
		t.Fatalf("filename=%s", backup.ArchiveFilename(manifest))
	}

	files := zipNames(t, raw)
	if _, ok := files["manifest.json"]; !ok {
		t.Fatal("missing manifest.json")
	}
	if _, ok := files["store.db"]; !ok {
		t.Fatal("missing store.db")
	}
	for rel := range seed {
		if rel == "api.token" || rel == "daemon.json" || localdata.IsCredentialRel(rel) {
			continue
		}
		if _, ok := files[rel]; !ok {
			t.Fatalf("missing %s", rel)
		}
	}
	for _, excluded := range []string{"api.token", "daemon.json", "store.db-wal", "store.db-shm", "store.db-journal"} {
		if _, ok := files[excluded]; ok {
			t.Fatalf("archive must not include runtime path %s", excluded)
		}
	}
	if _, ok := files["app-state-v1/6465627567.json"]; !ok {
		t.Fatal("missing app-state-v1 slice")
	}
	for _, wantDir := range []string{"app-state-v1", "projects", "source-content", "worker-baselines", "drafts", "session-checkpoints"} {
		if !slices.Contains(manifest.ReplaceRelDirs, wantDir) {
			t.Fatalf("replace_rel_dirs=%v missing %s", manifest.ReplaceRelDirs, wantDir)
		}
	}
	firstStage, err := backup.Stage(ctx, backup.StageOpts{
		ConfigDir: configDir, ArchivePath: archivePath(t, raw), SQLDB: sqlDB, SchemaVersion: db.SchemaVersion,
	})
	testutil.FailErr(t, "stage archive produced from running install", err)
	markerBefore, err := os.ReadFile(backup.PendingMarkerPath(configDir))
	testutil.FailErr(t, "read first pending marker", err)
	stagingBefore, err := filepath.Glob(filepath.Join(configDir, localdata.RestoreStagingDirPrefix+"-*"))
	testutil.FailErr(t, "list first staging", err)
	recoveryBefore, err := filepath.Glob(filepath.Join(configDir, localdata.RestorePreImageDirPrefix+"-*"))
	testutil.FailErr(t, "list first recovery", err)
	_, err = backup.Stage(ctx, backup.StageOpts{
		ConfigDir: configDir, ArchivePath: archivePath(t, raw), SQLDB: sqlDB, SchemaVersion: db.SchemaVersion,
	})
	if !errors.Is(err, backup.ErrPending) {
		t.Fatalf("second stage error = %v, want ErrPending", err)
	}
	markerAfter, err := os.ReadFile(backup.PendingMarkerPath(configDir))
	testutil.FailErr(t, "read retained pending marker", err)
	if !bytes.Equal(markerAfter, markerBefore) {
		t.Fatal("second stage replaced the first pending marker")
	}
	stagingAfter, err := filepath.Glob(filepath.Join(configDir, localdata.RestoreStagingDirPrefix+"-*"))
	testutil.FailErr(t, "list retained staging", err)
	recoveryAfter, err := filepath.Glob(filepath.Join(configDir, localdata.RestorePreImageDirPrefix+"-*"))
	testutil.FailErr(t, "list retained recovery", err)
	if len(stagingAfter) != len(stagingBefore) || len(recoveryAfter) != len(recoveryBefore) {
		t.Fatalf("second stage leaked transaction directories: staging %d->%d recovery %d->%d", len(stagingBefore), len(stagingAfter), len(recoveryBefore), len(recoveryAfter))
	}
	if _, err := os.Stat(firstStage.RecoveryCopyPath); err != nil {
		t.Fatalf("first recovery copy was removed: %v", err)
	}
}

func TestCreateLeavesExistingDestinationUntouched(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "store.db")
	sqlDB := testdbfixture.OpenPath(t, dbPath)

	destPath := filepath.Join(t.TempDir(), "backup.zip")
	const existing = "existing backup"
	testutil.FailErr(t, "seed destination", os.WriteFile(destPath, []byte(existing), 0o600))
	_, err := backup.Create(ctx, backup.CreateOpts{
		ConfigDir:         configDir,
		SQLDB:             sqlDB,
		DBPath:            dbPath,
		AppVersion:        "test",
		SchemaUserVersion: db.SchemaVersion,
	}, destPath)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("Create error = %v, want os.ErrExist", err)
	}
	raw, err := os.ReadFile(destPath)
	testutil.FailErr(t, "read existing destination", err)
	if string(raw) != existing {
		t.Fatalf("existing destination changed to %q", raw)
	}
}

func TestCreateNeverArchivesSecretStores(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "store.db")
	sqlDB := testdbfixture.OpenPath(t, dbPath)

	for _, rel := range append(localdata.CredentialRelPaths(), "daemon.json") {
		testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(configDir, rel), []byte("x\n"), 0o600))
	}
	testutil.FailErr(t, "seed app-state dir", os.MkdirAll(filepath.Join(configDir, "app-state-v1"), 0o700))
	testutil.FailErr(t, "seed app-state slice",
		os.WriteFile(filepath.Join(configDir, "app-state-v1", "state.json"), []byte("x\n"), 0o600))

	raw, _, err := createArchive(t, ctx, backup.CreateOpts{
		ConfigDir:         configDir,
		SQLDB:             sqlDB,
		DBPath:            dbPath,
		AppVersion:        "test",
		SchemaUserVersion: db.SchemaVersion,
		Now:               time.Now().UTC(),
	})
	testutil.FailErr(t, "create", err)
	files := zipNames(t, raw)
	for _, rel := range localdata.CredentialRelPaths() {
		if _, ok := files[rel]; ok {
			t.Fatalf("backup must never contain the secret store %s", rel)
		}
	}
	if _, ok := files["app-state-v1/state.json"]; !ok {
		t.Fatal("backup must include app-state-v1 slices")
	}
	if _, ok := files["daemon.json"]; ok {
		t.Fatal("backup must omit daemon.json")
	}
}

func TestStageRejectsCorruptAndTooNew(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "store.db")
	sqlDB := testdbfixture.OpenPath(t, dbPath)
	vaultPath := filepath.Join(configDir, "credential-vault.age")
	testutil.FailErr(t, "seed credential vault", os.WriteFile(vaultPath, []byte("keep\n"), 0o600))

	_, err := backup.Stage(ctx, backup.StageOpts{
		ConfigDir:     configDir,
		ArchivePath:   archivePath(t, []byte("not-a-zip")),
		SQLDB:         sqlDB,
		SchemaVersion: db.SchemaVersion,
	})
	if !errors.Is(err, backup.ErrInvalid) {
		t.Fatalf("corrupt want ErrInvalid, got %v", err)
	}
	if _, err := os.Stat(vaultPath); err != nil {
		t.Fatal("invalid archive must not touch existing files")
	}
	if _, err := os.Stat(backup.PendingMarkerPath(configDir)); !os.IsNotExist(err) {
		t.Fatal("invalid archive must not write pending marker")
	}

	incompatible, _, err := createArchive(t, ctx, backup.CreateOpts{
		ConfigDir:         configDir,
		SQLDB:             sqlDB,
		DBPath:            dbPath,
		AppVersion:        "test",
		SchemaUserVersion: db.SchemaVersion + 10,
		Now:               time.Now().UTC(),
	})
	testutil.FailErr(t, "create incompatible payload", err)
	rewritten := rewriteManifestSchema(t, incompatible, db.SchemaVersion+10)
	_, err = backup.Stage(ctx, backup.StageOpts{
		ConfigDir:     configDir,
		ArchivePath:   archivePath(t, rewritten),
		SQLDB:         sqlDB,
		SchemaVersion: db.SchemaVersion,
	})
	if !errors.Is(err, backup.ErrBaselineMismatch) {
		t.Fatalf("baseline mismatch want ErrBaselineMismatch, got %v", err)
	}
}

func TestStageRejectsUnmanifestedZipEntry(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "store.db")
	sqlDB := testdbfixture.OpenPath(t, dbPath)
	raw, _, err := createArchive(t, ctx, backup.CreateOpts{
		ConfigDir: configDir, SQLDB: sqlDB, DBPath: dbPath,
		AppVersion: "test", SchemaUserVersion: db.SchemaVersion,
	})
	testutil.FailErr(t, "create", err)

	_, err = backup.Stage(ctx, backup.StageOpts{
		ConfigDir: configDir, ArchivePath: archivePath(t, addZipEntry(t, raw, "unlisted.bin", "ignored")),
		SQLDB: sqlDB, SchemaVersion: db.SchemaVersion,
	})
	if !errors.Is(err, backup.ErrInvalid) {
		t.Fatalf("unmanifested entry err = %v want ErrInvalid", err)
	}
	if _, err := os.Stat(backup.PendingMarkerPath(configDir)); !os.IsNotExist(err) {
		t.Fatal("unmanifested entry wrote pending marker")
	}
}

func TestStageAndApplyRoundTrip(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "store.db")
	sqlDB, err := db.Open(dbPath)
	testutil.FailErr(t, "open", err)

	appStateDir := filepath.Join(configDir, "app-state-v1")
	testutil.FailErr(t, "seed app-state dir", os.MkdirAll(filepath.Join(appStateDir, "nested"), 0o700))
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(appStateDir, "nested", "prefs.json"), []byte(`{"v":1}`), 0o600))
	vaultPath := filepath.Join(configDir, "credential-vault.age")
	testutil.FailErr(t, "seed credential vault", os.WriteFile(vaultPath, []byte("encrypted-a"), 0o600))

	raw, _, err := createArchive(t, ctx, backup.CreateOpts{
		ConfigDir:         configDir,
		SQLDB:             sqlDB,
		DBPath:            dbPath,
		AppVersion:        "test",
		SchemaUserVersion: db.SchemaVersion,
		Now:               time.Now().UTC(),
	})
	testutil.FailErr(t, "create", err)
	_ = sqlDB.Close()

	sqlDB2, err := db.Open(dbPath)
	testutil.FailErr(t, "reopen", err)
	testutil.FailErr(t, "mutate", os.WriteFile(filepath.Join(appStateDir, "nested", "prefs.json"), []byte(`{"v":2}`), 0o600))
	testutil.FailErr(t, "add stale slice", os.WriteFile(filepath.Join(appStateDir, "stale.json"), []byte(`{}`), 0o600))
	testutil.FailErr(t, "add nested stale slice", os.WriteFile(filepath.Join(appStateDir, "nested", "stale.json"), []byte(`{}`), 0o600))
	testutil.FailErr(t, "mutate credential vault", os.WriteFile(vaultPath, []byte("encrypted-b"), 0o600))

	result, err := backup.Stage(ctx, backup.StageOpts{
		ConfigDir:     configDir,
		ArchivePath:   archivePath(t, raw),
		SQLDB:         sqlDB2,
		SchemaVersion: db.SchemaVersion,
	})
	testutil.FailErr(t, "stage", err)
	_ = sqlDB2.Close()
	if !result.RestartRequired {
		t.Fatal("expected restart_required")
	}
	if _, err := os.Stat(result.RecoveryCopyPath); err != nil {
		t.Fatalf("missing recovery copy: %v", err)
	}
	if _, err := os.Stat(backup.PendingMarkerPath(configDir)); err != nil {
		t.Fatal("pending marker missing after stage")
	}
	markerRaw, err := os.ReadFile(backup.PendingMarkerPath(configDir))
	testutil.FailErr(t, "read pending marker", err)
	var marker backup.PendingMarker
	testutil.FailErr(t, "decode pending marker", json.Unmarshal(markerRaw, &marker))
	// Live state changes only during apply.
	live, err := os.ReadFile(filepath.Join(appStateDir, "nested", "prefs.json"))
	testutil.FailErr(t, "read live", err)
	if string(live) != `{"v":2}` {
		t.Fatalf("stage must not overwrite live yet: %s", live)
	}

	testutil.FailErr(t, "apply", backup.ApplyPending(t.Context(), configDir))
	if _, err := os.Stat(backup.PendingMarkerPath(configDir)); !os.IsNotExist(err) {
		t.Fatal("marker must be cleared after apply")
	}
	if _, err := os.Stat(marker.StagingDir); !os.IsNotExist(err) {
		t.Fatal("staging must be cleared after apply")
	}
	restored, err := os.ReadFile(filepath.Join(appStateDir, "nested", "prefs.json"))
	testutil.FailErr(t, "read restored", err)
	if string(restored) != `{"v":1}` {
		t.Fatalf("apply restored app-state=%s", restored)
	}
	if _, err := os.Stat(filepath.Join(appStateDir, "stale.json")); !os.IsNotExist(err) {
		t.Fatalf("directory restore retained a slice absent from the archive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(appStateDir, "nested", "stale.json")); !os.IsNotExist(err) {
		t.Fatalf("directory restore retained a nested slice absent from the archive: %v", err)
	}
}

func TestRoundTripPreservesNestedHistoryDraftModesAndSymlinks(t *testing.T) {
	ctx := t.Context()
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "store.db")
	sqlDB, err := db.Open(dbPath)
	testutil.FailErr(t, "open store", err)

	files := map[string]struct {
		body string
		mode os.FileMode
	}{
		"projects/project-1/artifacts/a.png":                 {"artifact", 0o600},
		"projects/project-1/tool-output/result.txt":          {"tool result", 0o600},
		"source-content/ab/content-hash":                     {"retained source", 0o600},
		"worker-baselines/retained.db":                       {"retained worker baseline", 0o400},
		"drafts/project-1/scripts/run":                       {"#!/bin/sh\n", 0o755},
		"session-checkpoints/project-1/session-1/before.txt": {"before", 0o640},
	}
	for rel, fixture := range files {
		path := filepath.Join(configDir, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir "+rel, os.MkdirAll(filepath.Dir(path), 0o700))
		testutil.FailErr(t, "write "+rel, os.WriteFile(path, []byte(fixture.body), fixture.mode))
	}
	linkRel := "drafts/project-1/run"
	testutil.FailErr(t, "create draft symlink",
		os.Symlink("scripts/run", filepath.Join(configDir, filepath.FromSlash(linkRel))))

	raw, _, err := createArchive(t, ctx, backup.CreateOpts{
		ConfigDir: configDir, SQLDB: sqlDB, DBPath: dbPath,
		AppVersion: "test", SchemaUserVersion: db.SchemaVersion,
	})
	testutil.FailErr(t, "create", err)
	archiveFiles := zipNames(t, raw)
	for rel := range files {
		if _, ok := archiveFiles[rel]; !ok {
			t.Fatalf("archive missing durable history path %s", rel)
		}
	}
	if got := string(archiveFiles[linkRel]); got != "scripts/run" {
		t.Fatalf("archived symlink target = %q", got)
	}

	for _, dir := range localdata.BackupRelDirs() {
		testutil.FailErr(t, "clear "+dir, os.RemoveAll(filepath.Join(configDir, dir)))
	}
	testutil.FailErr(t, "seed blocking draft root", os.MkdirAll(filepath.Join(configDir, "drafts"), 0o700))
	testutil.FailErr(t, "seed blocking draft parent",
		os.WriteFile(filepath.Join(configDir, "drafts", "project-1"), []byte("blocking file"), 0o600))
	testutil.FailErr(t, "seed blocking source destination",
		os.MkdirAll(filepath.Join(configDir, "source-content", "ab", "content-hash"), 0o700))
	_, err = backup.Stage(ctx, backup.StageOpts{
		ConfigDir: configDir, ArchivePath: archivePath(t, raw),
		SQLDB: sqlDB, SchemaVersion: db.SchemaVersion,
	})
	testutil.FailErr(t, "stage", err)
	testutil.FailErr(t, "close store", sqlDB.Close())
	testutil.FailErr(t, "apply", backup.ApplyPending(t.Context(), configDir))

	for rel, fixture := range files {
		path := filepath.Join(configDir, filepath.FromSlash(rel))
		raw, err := os.ReadFile(path)
		testutil.FailErr(t, "read restored "+rel, err)
		if string(raw) != fixture.body {
			t.Fatalf("restored %s = %q want %q", rel, raw, fixture.body)
		}
		info, err := os.Stat(path)
		testutil.FailErr(t, "stat restored "+rel, err)
		if info.Mode().Perm() != fixture.mode.Perm() {
			t.Fatalf("restored %s mode = %o want %o", rel, info.Mode().Perm(), fixture.mode.Perm())
		}
	}
	target, err := os.Readlink(filepath.Join(configDir, filepath.FromSlash(linkRel)))
	testutil.FailErr(t, "read restored symlink", err)
	if target != "scripts/run" {
		t.Fatalf("restored symlink target = %q", target)
	}
}

func TestHistoryOnlyRestoreKeepsLocalCredentials(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "store.db")
	sqlDB, err := db.Open(dbPath)
	testutil.FailErr(t, "open", err)

	appStateDir := filepath.Join(configDir, "app-state-v1")
	testutil.FailErr(t, "seed app-state dir", os.MkdirAll(appStateDir, 0o700))
	testutil.FailErr(t, "seed state", os.WriteFile(filepath.Join(appStateDir, "prefs.json"), []byte(`{"v":1}`), 0o600))
	vaultPath := filepath.Join(configDir, "credential-vault.age")
	testutil.FailErr(t, "seed credential vault", os.WriteFile(vaultPath, []byte("encrypted-archive"), 0o600))

	raw, _, err := createArchive(t, ctx, backup.CreateOpts{
		ConfigDir:         configDir,
		SQLDB:             sqlDB,
		DBPath:            dbPath,
		AppVersion:        "test",
		SchemaUserVersion: db.SchemaVersion,
		Now:               time.Now().UTC(),
	})
	testutil.FailErr(t, "create", err)

	testutil.FailErr(t, "local credential vault", os.WriteFile(vaultPath, []byte("encrypted-local"), 0o600))
	testutil.FailErr(t, "mutate state", os.WriteFile(filepath.Join(appStateDir, "prefs.json"), []byte(`{"v":9}`), 0o600))

	_, err = backup.Stage(ctx, backup.StageOpts{
		ConfigDir:     configDir,
		ArchivePath:   archivePath(t, raw),
		SQLDB:         sqlDB,
		SchemaVersion: db.SchemaVersion,
	})
	testutil.FailErr(t, "stage", err)
	_ = sqlDB.Close()
	testutil.FailErr(t, "apply", backup.ApplyPending(t.Context(), configDir))

	credentials, err := os.ReadFile(vaultPath)
	testutil.FailErr(t, "read credential vault", err)
	if string(credentials) != "encrypted-local" {
		t.Fatalf("restore wiped credentials outside its replacement scope: %s", credentials)
	}
	state, err := os.ReadFile(filepath.Join(appStateDir, "prefs.json"))
	testutil.FailErr(t, "read state", err)
	if string(state) != `{"v":1}` {
		t.Fatalf("state not restored: %s", state)
	}
}

func TestFullRestoreDeletesAbsentManagedFiles(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "store.db")
	sqlDB, err := db.Open(dbPath)
	testutil.FailErr(t, "open", err)

	raw, _, err := createArchive(t, ctx, backup.CreateOpts{
		ConfigDir:         configDir,
		SQLDB:             sqlDB,
		DBPath:            dbPath,
		AppVersion:        "test",
		SchemaUserVersion: db.SchemaVersion,
		Now:               time.Now().UTC(),
	})
	testutil.FailErr(t, "create without limits", err)

	liveLimits := []byte("limits: {live: 1}\n")
	testutil.FailErr(t, "write later limits", os.WriteFile(filepath.Join(configDir, "limits.yaml"), liveLimits, 0o600))
	result, err := backup.Stage(ctx, backup.StageOpts{
		ConfigDir: configDir, ArchivePath: archivePath(t, raw), SQLDB: sqlDB, SchemaVersion: db.SchemaVersion,
	})
	testutil.FailErr(t, "stage", err)
	testutil.FailErr(t, "close", sqlDB.Close())

	recovered, err := os.ReadFile(filepath.Join(result.RecoveryCopyPath, "limits.yaml"))
	testutil.FailErr(t, "read deleted-file recovery copy", err)
	if !bytes.Equal(recovered, liveLimits) {
		t.Fatalf("recovery limits = %q want %q", recovered, liveLimits)
	}
	testutil.FailErr(t, "apply", backup.ApplyPending(t.Context(), configDir))
	if _, err := os.Stat(filepath.Join(configDir, "limits.yaml")); !os.IsNotExist(err) {
		t.Fatalf("full restore retained limits absent from archive: %v", err)
	}
}

func TestApplyClearsSidecarsLeftByTheOutgoingStore(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "store.db")
	sqlDB, err := db.Open(dbPath)
	testutil.FailErr(t, "open", err)

	insertProject(t, ctx, sqlDB, "from-archive")
	raw, _, err := createArchive(t, ctx, backup.CreateOpts{
		ConfigDir:         configDir,
		SQLDB:             sqlDB,
		DBPath:            dbPath,
		AppVersion:        "test",
		SchemaUserVersion: db.SchemaVersion,
		Now:               time.Now().UTC(),
	})
	testutil.FailErr(t, "create", err)

	// Keep outgoing history only in the journal.
	insertProject(t, ctx, sqlDB, "outgoing-only")
	staleWAL, err := os.ReadFile(dbPath + "-wal")
	testutil.FailErr(t, "read live wal", err)
	if len(staleWAL) == 0 {
		t.Fatal("expected a non-empty WAL for the outgoing store")
	}

	_, err = backup.Stage(ctx, backup.StageOpts{
		ConfigDir:     configDir,
		ArchivePath:   archivePath(t, raw),
		SQLDB:         sqlDB,
		SchemaVersion: db.SchemaVersion,
	})
	testutil.FailErr(t, "stage", err)
	_ = sqlDB.Close()

	// Simulate an uncheckpointed journal at startup.
	testutil.FailErr(t, "leave stale wal", os.WriteFile(dbPath+"-wal", staleWAL, 0o600))

	testutil.FailErr(t, "apply", backup.ApplyPending(t.Context(), configDir))

	for _, suffix := range db.StoreSidecarSuffixes() {
		if _, err := os.Stat(dbPath + suffix); !os.IsNotExist(err) {
			t.Fatalf("apply left %s beside the restored store", dbPath+suffix)
		}
	}

	reopened := testdbfixture.OpenPath(t, dbPath)
	ids := projectIDs(t, ctx, reopened)
	if !slices.Contains(ids, "from-archive") {
		t.Fatalf("restored store lost the archived history: %v", ids)
	}
	if slices.Contains(ids, "outgoing-only") {
		t.Fatalf("the outgoing store's WAL was replayed into the restored one: %v", ids)
	}
}

func TestStageSnapshotsEveryFileTheRestoreReplaces(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "store.db")
	sqlDB, err := db.Open(dbPath)
	testutil.FailErr(t, "open", err)

	archived := map[string]string{
		"approvals.yaml":          "rules: [archived]\n",
		"limits.yaml":             "limits: {archived: 1}\n",
		"app-state-v1/prefs.json": `{"v":"archived"}`,
	}
	for rel, body := range archived {
		abs := filepath.Join(configDir, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir "+rel, os.MkdirAll(filepath.Dir(abs), 0o700))
		testutil.FailErr(t, "seed "+rel, os.WriteFile(abs, []byte(body), 0o600))
	}
	raw, _, err := createArchive(t, ctx, backup.CreateOpts{
		ConfigDir:         configDir,
		SQLDB:             sqlDB,
		DBPath:            dbPath,
		AppVersion:        "test",
		SchemaUserVersion: db.SchemaVersion,
		Now:               time.Now().UTC(),
	})
	testutil.FailErr(t, "create", err)

	live := map[string]string{}
	for rel := range archived {
		body := strings.Replace(archived[rel], "archived", "live", 1)
		live[rel] = body
		testutil.FailErr(t, "mutate "+rel, os.WriteFile(filepath.Join(configDir, rel), []byte(body), 0o600))
	}

	result, err := backup.Stage(ctx, backup.StageOpts{
		ConfigDir:     configDir,
		ArchivePath:   archivePath(t, raw),
		SQLDB:         sqlDB,
		SchemaVersion: db.SchemaVersion,
	})
	testutil.FailErr(t, "stage", err)
	testutil.FailErr(t, "close", sqlDB.Close())

	for rel, want := range live {
		got, err := os.ReadFile(filepath.Join(result.RecoveryCopyPath, rel))
		testutil.FailErr(t, "read recovery "+rel, err)
		if string(got) != want {
			t.Fatalf("recovery copy of %s = %q want the pre-restore %q", rel, got, want)
		}
	}
	info, err := os.Stat(filepath.Join(result.RecoveryCopyPath, "store.db"))
	testutil.FailErr(t, "stat recovery store", err)
	if info.Size() == 0 {
		t.Fatal("recovery copy of the store is empty")
	}

	// Apply preserves the recovery copy.
	testutil.FailErr(t, "apply", backup.ApplyPending(t.Context(), configDir))
	for rel, want := range live {
		got, err := os.ReadFile(filepath.Join(result.RecoveryCopyPath, rel))
		testutil.FailErr(t, "read recovery after apply "+rel, err)
		if string(got) != want {
			t.Fatalf("apply disturbed the recovery copy of %s: %q", rel, got)
		}
	}
}

func insertProject(t *testing.T, ctx context.Context, sqlDB db.Handle, id string) {
	t.Helper()
	_, err := sqlDB.ExecContext(ctx,
		`INSERT INTO projects(id, last_opened_at, created_at) VALUES(?, 't', 't')`, id)
	testutil.FailErr(t, "insert project "+id, err)
}

func projectIDs(t *testing.T, ctx context.Context, sqlDB db.Handle) []string {
	t.Helper()
	rows, err := sqlDB.QueryContext(ctx, `SELECT id FROM projects ORDER BY id`)
	testutil.FailErr(t, "query projects", err)
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		testutil.FailErr(t, "scan project id", rows.Scan(&id))
		out = append(out, id)
	}
	testutil.FailErr(t, "iterate projects", rows.Err())
	return out
}

func TestApplyPendingNoopWithoutMarker(t *testing.T) {
	t.Parallel()
	testutil.FailErr(t, "noop", backup.ApplyPending(t.Context(), t.TempDir()))
}

func TestPackageDoesNotImportRedactor(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "backup")
	entries, err := os.ReadDir(root)
	testutil.FailErr(t, "readdir", err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, e.Name()))
		testutil.FailErr(t, "read", err)
		if strings.Contains(string(body), "github.com/lycaon/lycaon/internal/observability") {
			t.Fatalf("%s must not import observability (redactor)", e.Name())
		}
	}
}

func createArchive(t *testing.T, ctx context.Context, opts backup.CreateOpts) ([]byte, backup.Manifest, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup.zip")
	manifest, err := backup.Create(ctx, opts, path)
	if err != nil {
		return nil, backup.Manifest{}, err
	}
	raw, err := os.ReadFile(path)
	if err == nil {
		digest := sha256.Sum256(raw)
		if manifest.ArchiveSHA256 != hex.EncodeToString(digest[:]) {
			t.Fatalf("completed archive digest %q does not match payload", manifest.ArchiveSHA256)
		}
	}
	return raw, manifest, err
}

func archivePath(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup.zip")
	testutil.FailErr(t, "write archive fixture", os.WriteFile(path, raw, 0o600))
	return path
}

func zipNames(t *testing.T, raw []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	testutil.FailErr(t, "zip reader", err)
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		testutil.FailErr(t, "open entry", err)
		var buf bytes.Buffer
		_, err = buf.ReadFrom(rc)
		_ = rc.Close()
		testutil.FailErr(t, "read entry", err)
		out[f.Name] = buf.Bytes()
	}
	return out
}

func rewriteManifestSchema(t *testing.T, raw []byte, schema int) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	testutil.FailErr(t, "zip", err)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		rc, err := f.Open()
		testutil.FailErr(t, "open", err)
		var body bytes.Buffer
		_, err = body.ReadFrom(rc)
		_ = rc.Close()
		testutil.FailErr(t, "read", err)
		data := body.Bytes()
		if f.Name == "manifest.json" {
			var m backup.Manifest
			testutil.FailErr(t, "unmarshal", json.Unmarshal(data, &m))
			m.SchemaUserVersion = schema
			// The manifest has no self-hash.
			data, err = json.MarshalIndent(m, "", "  ")
			testutil.FailErr(t, "marshal", err)
		}
		w, err := zw.Create(f.Name)
		testutil.FailErr(t, "create", err)
		_, err = w.Write(data)
		testutil.FailErr(t, "write", err)
	}
	testutil.FailErr(t, "close", zw.Close())
	return buf.Bytes()
}

func addZipEntry(t *testing.T, raw []byte, name, body string) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	testutil.FailErr(t, "zip", err)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, entry := range zr.File {
		rc, err := entry.Open()
		testutil.FailErr(t, "open", err)
		w, err := zw.Create(entry.Name)
		testutil.FailErr(t, "create", err)
		_, copyErr := io.Copy(w, rc)
		_ = rc.Close()
		testutil.FailErr(t, "copy", copyErr)
	}
	w, err := zw.Create(name)
	testutil.FailErr(t, "create extra", err)
	_, err = w.Write([]byte(body))
	testutil.FailErr(t, "write extra", err)
	testutil.FailErr(t, "close", zw.Close())
	return buf.Bytes()
}
