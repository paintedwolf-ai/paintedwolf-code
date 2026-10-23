package backup_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	projectmodel "github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRetainedTreeBackupRestoresExistingManifestFormat(t *testing.T) {
	for _, undone := range []bool{false, true} {
		t.Run(map[bool]string{false: "deleted", true: "undone"}[undone], func(t *testing.T) {
			config, root := t.TempDir(), t.TempDir()
			dbPath := filepath.Join(config, "store.db")
			database := testdbfixture.OpenPath(t, dbPath)
			rootID := testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, root)
			project := &projectmodel.Project{ID: testdbseed.DefaultProjectID, Roots: []projectmodel.Root{{ID: rootID, ProjectID: testdbseed.DefaultProjectID, Path: root, IsPrimary: true}}}
			content := filepath.Join(config, "source-content")
			ledger := sourceledger.New(database, content)
			service := projectsource.NewSourceMutationService(database, ledger)
			tree := filepath.Join(root, "tree")
			testutil.FailErr(t, "seed empty directory", os.MkdirAll(filepath.Join(tree, "empty"), 0o750))
			testutil.FailErr(t, "seed binary", os.WriteFile(filepath.Join(tree, "file"), []byte{0, 255, 42}, 0o751))
			testutil.FailErr(t, "seed link", os.Symlink("file", filepath.Join(tree, "link")))
			tracked, err := ledger.TrackFile(t.Context(), sourceledger.TrackInput{ProjectID: project.ID, RootID: rootID, Path: "tree/file", Content: []byte{0, 255, 42}})
			testutil.FailErr(t, "track child history", err)
			id, err := service.RemoveEntry(t.Context(), sourceeffect.Removal{
				Record: sourceledger.RecordInput{RecordLocation: sourceledger.RecordLocation{RootID: rootID, Path: "tree"}, ProjectID: project.ID, Op: api.SourceChangeOpDelete, Origin: api.SourceChangeOriginAgent}, RootPath: root, Recursive: true,
				Change: sourcefeed.Change{ProjectID: project.ID, WorkspaceID: project.WorkspaceID(), WorkspaceKind: api.SourceWorkspaceKindProject},
			})
			testutil.FailErr(t, "remove tree", err)
			if undone {
				_, err = service.Undo(t.Context(), uuid.NewString(), project, projectsource.SourceHistoryMutationRequest{ExpectedEntryID: id})
				testutil.FailErr(t, "undo before backup", err)
			}
			raw, manifest, err := createArchive(t, t.Context(), backup.CreateOpts{ConfigDir: config, DBPath: dbPath, SQLDB: database, SchemaUserVersion: db.SchemaVersion})
			testutil.FailErr(t, "create backup", err)
			if manifest.FormatVersion != 1 {
				t.Fatalf("backup changed format: %d", manifest.FormatVersion)
			}
			for rel := range zipNames(t, raw) {
				if strings.HasPrefix(rel, "source-content/"+sourceledger.RecoveryTreeDirectory+"/") {
					t.Fatalf("private custody escaped manifest conversion: %s", rel)
				}
			}
			var liveBindings, liveCaptures int
			testutil.FailErr(t, "inspect live bindings", database.QueryRowContext(t.Context(), `SELECT count(*) FROM source_history_entries WHERE json_type(undo_plan_json,'$.retained_tree')='object'`).Scan(&liveBindings))
			testutil.FailErr(t, "inspect live captures", database.QueryRowContext(t.Context(), `SELECT count(*) FROM source_recovery_entries`).Scan(&liveCaptures))
			if liveBindings != 1 || liveCaptures != 0 {
				t.Fatalf("backup changed live recovery: bindings=%d captures=%d", liveBindings, liveCaptures)
			}
			_, err = backup.Stage(t.Context(), backup.StageOpts{ConfigDir: config, ArchivePath: archivePath(t, raw), SQLDB: database, SchemaVersion: db.SchemaVersion})
			testutil.FailErr(t, "stage backup", err)
			testutil.FailErr(t, "close original ledger", ledger.Snapshots.Close())
			testutil.FailErr(t, "close original store", database.Close())
			testutil.FailErr(t, "remove original custody", os.RemoveAll(content))
			testutil.FailErr(t, "apply backup", backup.ApplyPending(t.Context(), config))
			restored := testdbfixture.OpenPath(t, dbPath)
			restoredLedger := sourceledger.New(restored, content)
			t.Cleanup(func() { testutil.FailErr(t, "close restored ledger", restoredLedger.Snapshots.Close()) })
			service = projectsource.NewSourceMutationService(restored, restoredLedger)
			var bindings, captures int
			testutil.FailErr(t, "inspect restored bindings", restored.QueryRowContext(t.Context(), `SELECT count(*) FROM source_history_entries WHERE json_type(undo_plan_json,'$.retained_tree')='object'`).Scan(&bindings))
			testutil.FailErr(t, "inspect restored manifests", restored.QueryRowContext(t.Context(), `SELECT count(*) FROM source_recovery_entries`).Scan(&captures))
			if bindings != 0 || captures != 4 {
				t.Fatalf("restored recovery representation: bindings=%d captures=%d", bindings, captures)
			}
			if undone {
				_, err = service.Redo(t.Context(), uuid.NewString(), project, projectsource.SourceHistoryMutationRequest{ExpectedEntryID: id})
				testutil.FailErr(t, "redo restored history", err)
			}
			_, err = service.Undo(t.Context(), uuid.NewString(), project, projectsource.SourceHistoryMutationRequest{ExpectedEntryID: id})
			testutil.FailErr(t, "undo restored history", err)
			child, err := restoredLedger.History.ResolveHead(t.Context(), project.ID, sourcebranch.Trunk, rootID, "tree/file")
			testutil.FailErr(t, "resolve restored child history", err)
			if child.FileID != tracked.FileID || child.VersionID != tracked.VersionID {
				t.Fatalf("backup Undo forked child history: %+v, original %+v", child, tracked)
			}
			body, err := os.ReadFile(filepath.Join(tree, "file"))
			testutil.FailErr(t, "read restored binary", err)
			file, err := os.Stat(filepath.Join(tree, "file"))
			testutil.FailErr(t, "stat restored binary", err)
			empty, err := os.Stat(filepath.Join(tree, "empty"))
			testutil.FailErr(t, "stat restored empty directory", err)
			link, err := os.Readlink(filepath.Join(tree, "link"))
			testutil.FailErr(t, "read restored link", err)
			if string(body) != string([]byte{0, 255, 42}) || file.Mode().Perm() != 0o751 || !empty.IsDir() || empty.Mode().Perm() != 0o750 || link != "file" {
				t.Fatalf("restored content/modes: body=%v file=%v empty=%v link=%q", body, file, empty, link)
			}
		})
	}
}
