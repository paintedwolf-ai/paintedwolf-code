package backup_test

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRetainedReleaseArchiveCanStage(t *testing.T) {
	// One archive per released schema route; platform rehearsal keeps every release.
	fixtures := []struct {
		version string
		schema  int
	}{
		{"1.0.0", 1}, {"1.0.2-rc1", 2}, {"1.0.2", 4}, {"1.0.3-rc2", 5},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.version, func(t *testing.T) {
			archive := filepath.Join("..", "..", "testdata", "upgrade-corpus", fixture.version, "backup.zip")
			root := t.TempDir()
			path := filepath.Join(root, "store.db")
			store := testdbfixture.OpenPath(t, path)
			programs := filepath.Join(root, "command-programs.yaml")
			testutil.FailErr(t, "write current configured programs", os.WriteFile(programs, []byte("current programs\n"), 0600))
			result, err := backup.Stage(t.Context(), backup.StageOpts{
				ConfigDir: root, DBPath: path, SQLDB: store, SchemaVersion: db.SchemaVersion, ArchivePath: archive,
			})
			testutil.FailErr(t, "stage retained release archive without changing its bytes", err)
			recovery, err := os.ReadFile(filepath.Join(result.RecoveryCopyPath, "command-programs.yaml"))
			testutil.FailErr(t, "read configured program pre-image", err)
			if string(recovery) != "current programs\n" {
				t.Fatal("restore lost the current configured programs before applying")
			}
			reader, err := zip.OpenReader(archive)
			testutil.FailErr(t, "read frozen archive", err)
			manifestFile, err := reader.Open("manifest.json")
			testutil.FailErr(t, "open frozen archive manifest", err)
			var manifest backup.Manifest
			testutil.FailErr(t, "decode frozen archive manifest", json.NewDecoder(manifestFile).Decode(&manifest))
			testutil.FailErr(t, "close frozen manifest", manifestFile.Close())
			testutil.FailErr(t, "close frozen archive", reader.Close())
			if manifest.SchemaUserVersion != fixture.schema {
				t.Fatalf("frozen archive schema = %d, want route %d", manifest.SchemaUserVersion, fixture.schema)
			}
			absent := !slices.ContainsFunc(manifest.Files, func(file backup.FileEntry) bool { return file.RelPath == "command-programs.yaml" })
			marker := readMarker(t, root)
			if slices.Contains(marker.DeleteRelPaths, "command-programs.yaml") != absent {
				t.Fatal("restore transaction must preserve the archived presence or absence of configured programs")
			}
		})
	}
}
