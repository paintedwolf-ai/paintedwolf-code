package backup_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/historyretention"
	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEveryArchivedFileRequiresCompleteIntegrityBeforeStaging(t *testing.T) {
	testutil.SkipIfShort(t, "stages every corruption of every archived file; TestStageRejectsCorruptAndTooNew keeps the single-file rejection in short")
	source := t.TempDir()
	sourceDB := testdbfixture.OpenPath(t, filepath.Join(source, "store.db"))
	for _, rel := range localdata.BackupRelPaths() {
		if rel != "store.db" {
			body := []byte("{}\n")
			if rel == "approvals.yaml" {
				body = nil
			}
			if rel == historyretention.PolicyFilename {
				var err error
				body, err = json.Marshal(historyretention.DefaultPolicy())
				testutil.FailErr(t, "encode retention policy", err)
			}
			testutil.FailErr(t, "seed durable file", os.WriteFile(filepath.Join(source, rel), body, 0o600))
		}
	}
	appState := filepath.Join(source, "app-state-v1")
	testutil.FailErr(t, "create app-state directory", os.MkdirAll(appState, 0o700))
	testutil.FailErr(t, "seed app-state file", os.WriteFile(filepath.Join(appState, "fixture.json"), []byte(`{"fixture":true}`), 0o600))
	raw, manifest, err := createArchive(t, t.Context(), backup.CreateOpts{
		ConfigDir: source, SQLDB: sourceDB, DBPath: filepath.Join(source, "store.db"),
		AppVersion: "test", SchemaUserVersion: db.SchemaVersion,
	})
	testutil.FailErr(t, "create intact archive", err)
	_, err = backup.Stage(t.Context(), backup.StageOpts{
		ConfigDir: source, ArchivePath: archivePath(t, raw),
		SQLDB: sourceDB, SchemaVersion: db.SchemaVersion,
	})
	testutil.FailErr(t, "stage intact control archive", err)
	target := t.TempDir()
	targetDB := testdbfixture.OpenPath(t, filepath.Join(target, "store.db"))
	protected := protectedRestoreFiles(t, target)
	for index, entry := range manifest.Files {
		t.Run(entry.RelPath, func(t *testing.T) {
			for name, corrupt := range archiveIntegrityVariants(t, raw, index, entry.RelPath) {
				t.Run(name, func(t *testing.T) {
					_, stageErr := backup.Stage(t.Context(), backup.StageOpts{
						ConfigDir: target, ArchivePath: archivePath(t, corrupt),
						SQLDB: targetDB, SchemaVersion: db.SchemaVersion,
					})
					if !errors.Is(stageErr, backup.ErrInvalid) {
						t.Fatalf("corrupt archive error = %v, want ErrInvalid", stageErr)
					}
					assertRejectedRestorePreservesFiles(t, target, protected)
				})
			}
		})
	}
}

func archiveIntegrityVariants(t *testing.T, raw []byte, index int, path string) map[string][]byte {
	t.Helper()
	return map[string][]byte{
		"changed payload":   alterArchivedPayload(t, raw, path, false),
		"missing payload":   alterArchivedPayload(t, raw, path, true),
		"duplicate payload": addZipEntry(t, raw, path, string(zipNames(t, raw)[path])),
		"duplicate record": rewriteTestManifest(t, raw, func(m *backup.Manifest) {
			m.Files = append(m.Files, m.Files[index])
		}),
		"wrong size": rewriteTestManifest(t, raw, func(m *backup.Manifest) {
			m.Files[index].Size++
		}),
		"wrong digest": rewriteTestManifest(t, raw, func(m *backup.Manifest) {
			m.Files[index].SHA256 = strings.Repeat("0", 64)
		}),
	}
}

func alterArchivedPayload(t *testing.T, raw []byte, path string, omit bool) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	testutil.FailErr(t, "open archive for corruption", err)
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, entry := range zr.File {
		if entry.Name == path && omit {
			continue
		}
		r, openErr := entry.Open()
		testutil.FailErr(t, "open archive entry", openErr)
		body, readErr := io.ReadAll(r)
		testutil.FailErr(t, "close archive entry", r.Close())
		testutil.FailErr(t, "read archive entry", readErr)
		if entry.Name == path {
			if len(body) == 0 {
				body = []byte("changed")
			} else {
				body[len(body)/2] ^= 1
			}
		}
		w, createErr := zw.CreateHeader(&entry.FileHeader)
		testutil.FailErr(t, "create changed entry", createErr)
		_, writeErr := w.Write(body)
		testutil.FailErr(t, "write changed entry", writeErr)
	}
	testutil.FailErr(t, "close changed archive", zw.Close())
	return out.Bytes()
}

func protectedRestoreFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	for _, rel := range []string{"credential-vault.age", "limits.yaml"} {
		testutil.FailErr(t, "seed live protected file", os.WriteFile(filepath.Join(root, rel), []byte("keep live "+rel), 0o600))
	}
	files := make(map[string][]byte)
	for _, rel := range []string{"credential-vault.age", "limits.yaml", "store.db", "store.db-wal"} {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if rel == "store.db-wal" && os.IsNotExist(err) {
			continue
		}
		testutil.FailErr(t, "capture live file", err)
		files[rel] = data
	}
	return files
}

func assertRejectedRestorePreservesFiles(t *testing.T, root string, protected map[string][]byte) {
	t.Helper()
	for rel, want := range protected {
		got, err := os.ReadFile(filepath.Join(root, rel))
		testutil.FailErr(t, "read protected live file", err)
		if !bytes.Equal(got, want) {
			t.Errorf("rejected restore changed %s", rel)
		}
	}
	if _, err := os.Stat(backup.PendingMarkerPath(root)); !os.IsNotExist(err) {
		t.Fatalf("rejected restore left a pending marker: %v", err)
	}
	for _, prefix := range []string{localdata.RestoreStagingDirPrefix, localdata.RestorePreImageDirPrefix} {
		paths, err := filepath.Glob(filepath.Join(root, prefix+"-*"))
		testutil.FailErr(t, "inspect rejected restore directories", err)
		if len(paths) != 0 {
			t.Errorf("rejected restore left transaction directories: %v", paths)
		}
	}
}
