package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/internal/testutil"
)

// controlPlaneRelPaths are excluded from restore.
var controlPlaneRelPaths = []string{
	"mcp.yaml",
	"extensions.yaml",
	"packs/evil/pack.yaml",
	"verify.yaml",
	"standing-patterns.yaml",
	// Live process state and device identity.
	"api.token",
	"host-identity.pem",
	"daemon.json",
	// Credential stores.
	"credential-vault.age",
	"credential-vault-identity.age",
	".credential-vault-development-identity",
	// Database journals.
	"store.db-wal",
	"store.db-shm",
	"store.db-journal",
}

// restorableRelPaths pin the archive's durable set.
var restorableRelPaths = []string{
	"store.db",
	"approvals.yaml",
	"limits.yaml",
}

// restorableDirChildRelPaths pin the per-file entries under durable directories.
var restorableDirChildRelPaths = []string{
	"app-state-v1/prefs.json",
	"app-state-v1/6465627567.json",
}

func stageArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	shapeDigest, err := db.BaselineShapeDigest(context.Background())
	testutil.FailErr(t, "baseline shape digest", err)
	manifest := Manifest{
		FormatVersion: FormatVersion, AppVersion: "test", SchemaUserVersion: db.SchemaVersion,
		SchemaShapeDigest: shapeDigest,
		CreatedAt:         "2026-01-01T00:00:00Z",
		ReplaceRelPaths:   localdata.BackupRelPaths(),
		ReplaceRelDirs:    localdata.BackupRelDirs(),
	}
	for name, body := range files {
		w, err := zw.Create(name)
		testutil.FailErr(t, "zip create", err)
		_, err = w.Write([]byte(body))
		testutil.FailErr(t, "zip write", err)
		manifest.Files = append(manifest.Files, FileEntry{
			RelPath: name, Kind: fileKindRegular, Mode: uint32(fileMode),
			Size: int64(len(body)), SHA256: sha256Hex([]byte(body)),
		})
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	testutil.FailErr(t, "marshal manifest", err)
	mw, err := zw.Create("manifest.json")
	testutil.FailErr(t, "zip create manifest", err)
	_, err = mw.Write(raw)
	testutil.FailErr(t, "zip write manifest", err)
	testutil.FailErr(t, "zip close", zw.Close())
	return buf.Bytes()
}

func writeArchiveFixture(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup.zip")
	testutil.FailErr(t, "write archive fixture", os.WriteFile(path, raw, 0o600))
	return path
}

func TestRestorableRelPathTracksTheBackupSet(t *testing.T) {
	// The producer and installer share one durable set.
	for _, rel := range restorableRelPaths {
		if !RestorableRelPath(rel) {
			t.Errorf("RestorableRelPath(%q) = false, want true — a restore is for exactly this", rel)
		}
		if !slices.Contains(localdata.BackupRelPaths(), rel) {
			t.Errorf("%q is restorable but Create never writes it", rel)
		}
	}
	for _, rel := range restorableDirChildRelPaths {
		if !RestorableRelPath(rel) {
			t.Errorf("RestorableRelPath(%q) = false, want true — durable-directory files restore per file", rel)
		}
	}
	if !RestorableRelDir("app-state-v1") {
		t.Error(`RestorableRelDir("app-state-v1") = false, want true`)
	}
	for _, rel := range []string{"limits.yaml", "other-dir", ""} {
		if RestorableRelDir(rel) {
			t.Errorf("RestorableRelDir(%q) = true, want false", rel)
		}
	}
	for _, rel := range controlPlaneRelPaths {
		if RestorableRelPath(rel) {
			t.Errorf("RestorableRelPath(%q) = true, want false — not part of a backup", rel)
		}
	}
	if !RestorableRelPath("app-state-v1/nested/slice.json") {
		t.Error("nested durable-directory file is not restorable")
	}
	for _, rel := range []string{"app-state-v1", "other-dir/x.json"} {
		if RestorableRelPath(rel) {
			t.Errorf("RestorableRelPath(%q) = true, want false", rel)
		}
	}
	// Alternate spellings remain invalid.
	for _, rel := range []string{"./mcp.yaml", "packs/../mcp.yaml", "app-state-v1/../credential-vault.age", "", "."} {
		if RestorableRelPath(rel) {
			t.Errorf("RestorableRelPath(%q) = true, want false", rel)
		}
	}
}

func TestStageRejectsArchiveNamingControlPlaneFiles(t *testing.T) {
	for _, rel := range controlPlaneRelPaths {
		cfg := t.TempDir()
		archive := stageArchive(t, map[string]string{rel: "flag: true\n"})
		_, err := Stage(context.Background(), StageOpts{
			ConfigDir:     cfg,
			ArchivePath:   writeArchiveFixture(t, archive),
			SchemaVersion: 999,
		})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("Stage(%q) err = %v, want ErrInvalid", rel, err)
		}
		if _, statErr := os.Stat(filepath.Join(cfg, rel)); statErr == nil {
			t.Errorf("Stage(%q) installed the file despite rejecting the archive", rel)
		}
	}
}

func TestStageRejectsPartialReplacementScope(t *testing.T) {
	raw := stageArchive(t, map[string]string{"store.db": "not reached"})
	archive := rewriteArchiveManifest(t, raw, func(manifest *Manifest) {
		manifest.ReplaceRelDirs = manifest.ReplaceRelDirs[:1]
	})
	_, err := Stage(t.Context(), StageOpts{
		ConfigDir: t.TempDir(), ArchivePath: writeArchiveFixture(t, archive), SchemaVersion: db.SchemaVersion,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Stage() error = %v, want ErrInvalid", err)
	}
}

func rewriteArchiveManifest(t *testing.T, raw []byte, update func(*Manifest)) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	testutil.FailErr(t, "open archive", err)
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, entry := range zr.File {
		rc, openErr := entry.Open()
		testutil.FailErr(t, "open entry", openErr)
		data, readErr := io.ReadAll(rc)
		_ = rc.Close()
		testutil.FailErr(t, "read entry", readErr)
		if entry.Name == "manifest.json" {
			var manifest Manifest
			testutil.FailErr(t, "decode manifest", json.Unmarshal(data, &manifest))
			update(&manifest)
			data, err = json.MarshalIndent(manifest, "", "  ")
			testutil.FailErr(t, "encode manifest", err)
		}
		w, createErr := zw.Create(entry.Name)
		testutil.FailErr(t, "create entry", createErr)
		_, writeErr := w.Write(data)
		testutil.FailErr(t, "write entry", writeErr)
	}
	testutil.FailErr(t, "close archive", zw.Close())
	return out.Bytes()
}

func TestWriteMarkerExclusivePreservesFirstMarker(t *testing.T) {
	path := PendingMarkerPath(t.TempDir())
	first := PendingMarker{LiveStoreFilename: storeRelPath, Operation: PendingOperationRestore, StagingDir: "/first", CreatedAt: "first"}
	second := PendingMarker{LiveStoreFilename: storeRelPath, Operation: PendingOperationRestore, StagingDir: "/second", CreatedAt: "second"}
	testutil.FailErr(t, "publish first marker", writeMarkerExclusive(path, first))
	if err := writeMarkerExclusive(path, second); !os.IsExist(err) {
		t.Fatalf("second publication error = %v, want existence conflict", err)
	}
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read marker", err)
	var got PendingMarker
	testutil.FailErr(t, "decode marker", json.Unmarshal(raw, &got))
	if got.StagingDir != first.StagingDir {
		t.Fatalf("exclusive publication replaced first marker: %+v", got)
	}
}

func TestStageAcceptsTheDurableSet(t *testing.T) {
	cfg := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "store.db")
	sqlDB, err := db.Open(dbPath)
	testutil.FailErr(t, "open fixture store", err)
	testutil.FailErr(t, "close fixture store", sqlDB.Close())
	storeRaw, err := os.ReadFile(dbPath)
	testutil.FailErr(t, "read fixture store", err)
	archive := stageArchive(t, map[string]string{
		"store.db":                       string(storeRaw),
		"limits.yaml":                    "max_iterations: 5\n",
		"app-state-v1/nested/prefs.json": "{}\n",
	})
	_, err = Stage(context.Background(), StageOpts{
		ConfigDir:     cfg,
		ArchivePath:   writeArchiveFixture(t, archive),
		SchemaVersion: db.SchemaVersion,
	})
	testutil.FailErr(t, "Stage durable set", err)
	testutil.FailErr(t, "ApplyPending", ApplyPending(cfg))
	for _, rel := range []string{"store.db", "limits.yaml", "app-state-v1/nested/prefs.json"} {
		if _, err := os.Stat(filepath.Join(cfg, filepath.FromSlash(rel))); err != nil {
			t.Errorf("durable file %q not installed: %v", rel, err)
		}
	}
}

func TestApplyPendingRefusesAMarkerNamingAControlPlaneFile(t *testing.T) {
	cfg := t.TempDir()
	staging, recovery := makeRestoreTransactionDirs(t, cfg, "7da85e16-85da-4c89-9aa6-9aed13fcdcf9")
	body := []byte("providers:\n  - id: pwn\n    command: /bin/sh\n")
	testutil.FailErr(t, "write staged", os.WriteFile(filepath.Join(staging, "mcp.yaml"), body, 0o600))

	marker := PendingMarker{
		LiveStoreFilename: storeRelPath,
		Operation:         PendingOperationRestore,
		StagingDir:        staging,
		RecoveryDir:       recovery,
		Files: []PendingFile{{
			RelPath: "mcp.yaml", Kind: fileKindRegular, Mode: uint32(fileMode),
			SHA256: sha256Hex(body), Size: int64(len(body)),
		}},
	}
	raw, err := json.MarshalIndent(marker, "", "  ")
	testutil.FailErr(t, "marshal marker", err)
	testutil.FailErr(t, "write marker", os.WriteFile(PendingMarkerPath(cfg), raw, 0o600))

	if err := ApplyPending(cfg); err == nil {
		t.Fatal("ApplyPending accepted a marker naming mcp.yaml")
	}
	if _, err := os.Stat(filepath.Join(cfg, "mcp.yaml")); err == nil {
		t.Fatal("ApplyPending installed mcp.yaml")
	}
}

func TestApplyPendingRefusesProgressOutsidePendingOperations(t *testing.T) {
	cfg := t.TempDir()
	staging, recovery := makeRestoreTransactionDirs(t, cfg, "6f1eb4aa-9125-4bc9-87bf-f4658c7abfa4")
	body := []byte("max_iterations: 5\n")
	testutil.FailErr(t, "write staged", os.WriteFile(filepath.Join(staging, "limits.yaml"), body, 0o600))

	marker := PendingMarker{
		LiveStoreFilename: storeRelPath,
		Operation:         PendingOperationRestore,
		StagingDir:        staging,
		RecoveryDir:       recovery,
		Files: []PendingFile{{
			RelPath: "limits.yaml", Kind: fileKindRegular, Mode: uint32(fileMode),
			SHA256: sha256Hex(body), Size: int64(len(body)),
		}},
		Applied: []string{"approvals.yaml"},
	}
	raw, err := json.MarshalIndent(marker, "", "  ")
	testutil.FailErr(t, "marshal marker", err)
	testutil.FailErr(t, "write marker", os.WriteFile(PendingMarkerPath(cfg), raw, 0o600))

	if err := ApplyPending(cfg); err == nil {
		t.Fatal("ApplyPending accepted progress outside the pending operations")
	}
	if _, err := os.Stat(filepath.Join(cfg, "limits.yaml")); err == nil {
		t.Fatal("ApplyPending mutated live state before validating progress")
	}
}

func TestApplyPendingRequiresAnOperation(t *testing.T) {
	cfg := t.TempDir()
	staging, recovery := makeRestoreTransactionDirs(t, cfg, "d11335bc-e7ef-4d0f-aa64-181d1bf87253")
	body := []byte("max_iterations: 5\n")
	testutil.FailErr(t, "write staged", os.WriteFile(filepath.Join(staging, "limits.yaml"), body, 0o600))

	marker := PendingMarker{
		LiveStoreFilename: storeRelPath,
		StagingDir:        staging, RecoveryDir: recovery,
		Files: []PendingFile{{
			RelPath: "limits.yaml", Kind: fileKindRegular, Mode: uint32(fileMode),
			SHA256: sha256Hex(body), Size: int64(len(body)),
		}},
	}
	raw, err := json.MarshalIndent(marker, "", "  ")
	testutil.FailErr(t, "marshal marker", err)
	testutil.FailErr(t, "write marker", os.WriteFile(PendingMarkerPath(cfg), raw, 0o600))

	err = ApplyPending(cfg)
	if err == nil || !strings.Contains(err.Error(), "unknown pending operation") {
		t.Fatalf("ApplyPending error = %v, want missing operation rejection", err)
	}
	if _, err := os.Stat(filepath.Join(cfg, "limits.yaml")); !os.IsNotExist(err) {
		t.Fatalf("ApplyPending mutated live state: %v", err)
	}
}

func TestApplyPendingRequiresConfiguredStoreFilename(t *testing.T) {
	cfg := t.TempDir()
	staging, recovery := makeRestoreTransactionDirs(t, cfg, "b0e6544e-4ac0-41f2-8fc4-385d1c1aee59")
	marker := PendingMarker{Operation: PendingOperationRestore, StagingDir: staging, RecoveryDir: recovery}
	raw, err := json.Marshal(marker)
	testutil.FailErr(t, "marshal missing-target marker", err)
	testutil.FailErr(t, "write missing-target marker", os.WriteFile(PendingMarkerPath(cfg), raw, 0o600))
	if err := ApplyPending(cfg); err == nil || !strings.Contains(err.Error(), "invalid configured store filename") {
		t.Fatalf("ApplyPending error = %v, want missing store target rejection", err)
	}
}

func TestApplyPendingRefusesStagingOutsideConfigRoot(t *testing.T) {
	cfg := t.TempDir()
	externalRoot := t.TempDir()
	const id = "8d06c1cb-406c-4f26-8744-754f82bd2969"
	staging := filepath.Join(externalRoot, localdata.RestoreStagingDirPrefix+"-"+id)
	testutil.FailErr(t, "mkdir external staging", os.MkdirAll(staging, 0o700))
	recovery := filepath.Join(cfg, localdata.RestorePreImageDirPrefix+"-"+id)
	testutil.FailErr(t, "mkdir recovery", os.MkdirAll(recovery, 0o700))
	body := []byte("max_iterations: 5\n")
	externalFile := filepath.Join(staging, "limits.yaml")
	testutil.FailErr(t, "write external staging", os.WriteFile(externalFile, body, 0o600))

	marker := PendingMarker{
		LiveStoreFilename: storeRelPath,
		Operation:         PendingOperationRestore,
		StagingDir:        staging,
		RecoveryDir:       recovery,
		Files: []PendingFile{{
			RelPath: "limits.yaml", Kind: fileKindRegular, Mode: uint32(fileMode),
			SHA256: sha256Hex(body), Size: int64(len(body)),
		}},
	}
	raw, err := json.MarshalIndent(marker, "", "  ")
	testutil.FailErr(t, "marshal marker", err)
	testutil.FailErr(t, "write marker", os.WriteFile(PendingMarkerPath(cfg), raw, 0o600))

	if err := ApplyPending(cfg); err == nil {
		t.Fatal("ApplyPending accepted staging outside the config root")
	}
	_, err = os.Stat(externalFile)
	testutil.FailErr(t, "stat external staging", err)
	if _, err := os.Stat(filepath.Join(cfg, "limits.yaml")); !os.IsNotExist(err) {
		t.Fatalf("ApplyPending mutated live state: %v", err)
	}
}

func TestApplyPendingRefusesMismatchedTransactionDirectories(t *testing.T) {
	cfg := t.TempDir()
	staging, _ := makeRestoreTransactionDirs(t, cfg, "2fdb5d66-23cb-4591-b2e2-b3503b4dc97f")
	_, recovery := makeRestoreTransactionDirs(t, cfg, "ee64e83f-4cd7-4435-b6f8-c4612fcce21d")
	marker := PendingMarker{
		LiveStoreFilename: storeRelPath,
		Operation:         PendingOperationRestore, StagingDir: staging, RecoveryDir: recovery,
	}
	raw, err := json.MarshalIndent(marker, "", "  ")
	testutil.FailErr(t, "marshal marker", err)
	testutil.FailErr(t, "write marker", os.WriteFile(PendingMarkerPath(cfg), raw, 0o600))

	if err := ApplyPending(cfg); err == nil {
		t.Fatal("ApplyPending accepted mismatched transaction directories")
	}
}

func makeRestoreTransactionDirs(t *testing.T, configDir, id string) (string, string) {
	t.Helper()
	staging := filepath.Join(configDir, localdata.RestoreStagingDirPrefix+"-"+id)
	recovery := filepath.Join(configDir, localdata.RestorePreImageDirPrefix+"-"+id)
	testutil.FailErr(t, "mkdir staging", os.MkdirAll(staging, 0o700))
	testutil.FailErr(t, "mkdir recovery", os.MkdirAll(recovery, 0o700))
	return staging, recovery
}
