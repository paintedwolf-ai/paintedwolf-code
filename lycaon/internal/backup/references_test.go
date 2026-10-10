package backup_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/zstdcodec"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBackupRequiresRetainedArtifactAndIgnoresTombstone(t *testing.T) {
	config := t.TempDir()
	dbPath := filepath.Join(config, "store.db")
	database := testdbfixture.OpenPath(t, dbPath)
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	body := []byte("retained artifact")
	hash := textfile.SHA256(body)
	encoded, err := zstdcodec.Compress(bytes.NewReader(body))
	testutil.FailErr(t, "encode retained artifact", err)
	_, err = database.ExecContext(t.Context(), `INSERT INTO artifacts
		(id,project_id,content_hash,byte_size,stored_size,mime,source,created_at,updated_at)
		VALUES ('artifact',?,?,?,?,'image/png','user','2026-09-10','2026-09-10')`, testdbseed.DefaultProjectID, hash, len(body), len(encoded))
	testutil.FailErr(t, "insert retained artifact", err)
	opts := backup.CreateOpts{ConfigDir: config, DBPath: dbPath, SQLDB: database, SchemaUserVersion: db.SchemaVersion}
	destination := filepath.Join(t.TempDir(), "backup.zip")
	_, err = backup.Create(t.Context(), opts, destination)
	if !errors.Is(err, backup.ErrInvalid) {
		t.Fatalf("missing retained file error = %v", err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("failed backup published destination: %v", err)
	}
	rel := filepath.ToSlash(filepath.Join("projects", testdbseed.DefaultProjectID, "artifacts", hash))
	path := filepath.Join(config, filepath.FromSlash(rel))
	testutil.FailErr(t, "create artifact directory", os.MkdirAll(filepath.Dir(path), 0o700))
	testutil.FailErr(t, "write corrupt artifact", os.WriteFile(path, bytes.Repeat([]byte{'x'}, len(encoded)), 0o600))
	_, err = backup.Create(t.Context(), opts, destination)
	if !errors.Is(err, backup.ErrInvalid) {
		t.Fatalf("unreadable compressed artifact error = %v", err)
	}
	testutil.FailErr(t, "write artifact", os.WriteFile(path, encoded, 0o600))
	raw, manifest, err := createArchive(t, t.Context(), opts)
	testutil.FailErr(t, "create complete archive", err)
	// The ZIP matches its manifest but omits a file referenced by the captured database.
	incomplete := omitRetainedFile(t, raw, manifest, rel)
	archivePath := filepath.Join(t.TempDir(), "incomplete.zip")
	testutil.FailErr(t, "write incomplete archive", os.WriteFile(archivePath, incomplete, 0o600))
	failedMarker := []byte(`{"failed_at":"2026-09-10T00:00:00Z"}`)
	testutil.FailErr(t, "seed failed restore marker", os.WriteFile(backup.PendingMarkerPath(config), failedMarker, 0o600))
	_, err = backup.Stage(t.Context(), backup.StageOpts{ConfigDir: config, ArchivePath: archivePath, SQLDB: database, SchemaVersion: db.SchemaVersion})
	if !errors.Is(err, backup.ErrInvalid) {
		t.Fatalf("incomplete restore error = %v", err)
	}
	retainedMarker, err := os.ReadFile(backup.PendingMarkerPath(config))
	testutil.FailErr(t, "read prior failed restore marker", err)
	if !bytes.Equal(retainedMarker, failedMarker) {
		t.Fatalf("invalid archive changed the pending transaction: %s", retainedMarker)
	}

	_, err = database.ExecContext(t.Context(), `UPDATE artifacts SET deleted_at='2026-09-10' WHERE id='artifact'`)
	testutil.FailErr(t, "tombstone artifact", err)
	testutil.FailErr(t, "remove tombstoned content", os.Remove(path))
	_, err = backup.Create(t.Context(), opts, destination)
	testutil.FailErr(t, "backup with intentionally deleted content", err)
}

func omitRetainedFile(t *testing.T, raw []byte, manifest backup.Manifest, omitted string) []byte {
	t.Helper()
	files := manifest.Files[:0]
	for _, entry := range manifest.Files {
		if entry.RelPath != omitted {
			files = append(files, entry)
		}
	}
	manifest.Files = files
	encoded, err := json.Marshal(manifest)
	testutil.FailErr(t, "encode incomplete manifest", err)
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	testutil.FailErr(t, "read archive", err)
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, file := range reader.File {
		if file.Name == omitted {
			continue
		}
		dest, err := writer.Create(file.Name)
		testutil.FailErr(t, "create archive entry", err)
		if file.Name == "manifest.json" {
			_, err = dest.Write(encoded)
		} else {
			src, openErr := file.Open()
			testutil.FailErr(t, "open archive entry", openErr)
			_, err = io.Copy(dest, src)
			testutil.FailErr(t, "close archive entry", src.Close())
		}
		testutil.FailErr(t, "write archive entry", err)
	}
	testutil.FailErr(t, "close archive", writer.Close())
	return output.Bytes()
}

func TestBackupRequiresStoredSourceButAllowsMetadataOnly(t *testing.T) {
	config := t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(config, "store.db"))
	rootID := testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	ledger := sourceledger.New(database, filepath.Join(config, "source-content"))
	input := sourceledger.RecordInput{ProjectID: testdbseed.DefaultProjectID, RootID: rootID,
		Path: "large.txt", Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent,
		AfterSHA256: textfile.SHA256([]byte("metadata only")), AfterSize: 13}
	testutil.FailErr(t, "record metadata-only revision", ledger.Record(t.Context(), input))
	opts := backup.CreateOpts{ConfigDir: config, DBPath: filepath.Join(config, "store.db"), SQLDB: database, SchemaUserVersion: db.SchemaVersion}
	_, err := backup.Create(t.Context(), opts, filepath.Join(t.TempDir(), "metadata.zip"))
	testutil.FailErr(t, "backup metadata-only revision", err)
	input.Path, input.After = "small.txt", []byte("retained source")
	input.AfterSHA256, input.AfterSize = textfile.SHA256(input.After), int64(len(input.After))
	testutil.FailErr(t, "record retained revision", ledger.Record(t.Context(), input))
	_, err = backup.Create(t.Context(), opts, filepath.Join(t.TempDir(), "complete.zip"))
	testutil.FailErr(t, "backup stored revision", err)
	rel, err := sourceblob.RelPath(input.AfterSHA256)
	testutil.FailErr(t, "resolve retained source", err)
	testutil.FailErr(t, "remove required source object", os.Remove(filepath.Join(config, "source-content", rel)))
	_, err = backup.Create(t.Context(), opts, filepath.Join(t.TempDir(), "incomplete.zip"))
	if !errors.Is(err, backup.ErrInvalid) {
		t.Fatalf("missing stored revision error = %v", err)
	}
}

func TestFreshStartCanReplaceSemanticallyInvalidRestoreMarker(t *testing.T) {
	config := t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(config, "store.db"))
	testutil.FailErr(t, "seed invalid restore marker", os.WriteFile(backup.PendingMarkerPath(config), []byte(`null`), 0o600))
	result, err := backup.StageFreshStart(t.Context(), backup.FreshStartOpts{
		ConfigDir: config, DBPath: filepath.Join(config, "store.db"), SQLDB: database,
	})
	testutil.FailErr(t, "replace invalid marker", err)
	if !result.RestartRequired {
		t.Fatal("replacement did not stage a fresh start")
	}
	raw, err := os.ReadFile(backup.PendingMarkerPath(config))
	testutil.FailErr(t, "read replacement marker", err)
	var marker backup.PendingMarker
	testutil.FailErr(t, "decode replacement marker", json.Unmarshal(raw, &marker))
	if marker.Operation != backup.PendingOperationFreshStart || marker.StagingDir == "" {
		t.Fatalf("replacement marker = %+v", marker)
	}
}

func TestPopulatedBackupRestoresRetainedFiles(t *testing.T) {
	config := t.TempDir()
	dbPath := filepath.Join(config, "store.db")
	database := testdbfixture.OpenPath(t, dbPath)
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	filesDir := filepath.Join(config, "projects", testdbseed.DefaultProjectID, "evidence")
	testutil.FailErr(t, "create evidence directory", os.MkdirAll(filesDir, 0o700))
	for i := range 32 {
		path := filepath.Join(filesDir, fmt.Sprintf("capture-%02d.txt", i))
		testutil.FailErr(t, "write retained evidence", os.WriteFile(path, []byte("captured"), 0o600))
	}
	archive := filepath.Join(t.TempDir(), "backup.zip")
	_, err := backup.Create(t.Context(), backup.CreateOpts{
		ConfigDir: config, DBPath: dbPath, SQLDB: database, SchemaUserVersion: db.SchemaVersion,
	}, archive)
	testutil.FailErr(t, "capture populated archive", err)
	testutil.FailErr(t, "edit after capture", os.WriteFile(filepath.Join(filesDir, "capture-00.txt"), []byte("later"), 0o600))
	marker := backup.PendingMarkerPath(config)
	testutil.FailErr(t, "seed failed restore", os.WriteFile(marker, []byte(`{"failed_at":"2026-09-10T00:00:00Z"}`), 0o600))
	result, err := backup.Stage(t.Context(), backup.StageOpts{
		ConfigDir: config, ArchivePath: archive, SQLDB: database, SchemaVersion: db.SchemaVersion,
	})
	testutil.FailErr(t, "stage populated archive", err)
	if !result.RestartRequired {
		t.Fatal("restore was not staged")
	}
	testutil.FailErr(t, "close live store", database.Shutdown(t.Context()))
	alias := filepath.Join(t.TempDir(), "config")
	testutil.FailErr(t, "alias configuration directory", os.Symlink(config, alias))
	testutil.FailErr(t, "apply populated archive through alias", backup.ApplyPending(t.Context(), alias))
	for i := range 32 {
		body, err := os.ReadFile(filepath.Join(filesDir, fmt.Sprintf("capture-%02d.txt", i)))
		testutil.FailErr(t, "read restored evidence", err)
		if string(body) != "captured" {
			t.Fatalf("restored evidence %d = %q", i, body)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("completed restore marker remains: %v", err)
	}
}
