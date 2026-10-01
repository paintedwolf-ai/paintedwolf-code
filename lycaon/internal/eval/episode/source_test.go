package episode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestDeliveredIdentityTracksExactSnapshotAfterOriginalRootIsGone(t *testing.T) {
	capture, project, delivery := t.TempDir(), t.TempDir(), t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(capture, "store.db"))
	snapshots := sourcesnapshot.New(database, sourceblob.New(filepath.Join(capture, "content")), filepath.Join(capture, "observations.db"),
		backgroundwork.New(map[backgroundwork.Resource]backgroundwork.Limits{backgroundwork.ResourceIO: {Total: 1}}))
	t.Cleanup(func() { testutil.FailErr(t, "close snapshots", snapshots.Close()) })
	body := []byte("DEFAULT_LIMIT = 3\n")
	for _, root := range []string{project, delivery} {
		testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(root, "report.py"), body, 0o600))
	}
	snapshot, err := snapshots.Ensure(t.Context(), sourcesnapshot.Request{Roots: []sourcesnapshot.Root{{Path: project}}})
	testutil.FailErr(t, "capture source", err)
	testutil.FailErr(t, "remove original file", os.Remove(filepath.Join(project, "report.py")))
	_, err = database.ExecContext(t.Context(), `PRAGMA wal_checkpoint(TRUNCATE)`)
	testutil.FailErr(t, "checkpoint source", err)
	facts := Evidence{}
	testutil.FailErr(t, "compare copied delivery", facts.ReadSources(t.Context(), capture, delivery))
	if !facts.Snapshots[snapshot.ID].MatchesDelivered {
		t.Fatal("identical retained delivery rejected")
	}
	testutil.FailErr(t, "change delivery", os.WriteFile(filepath.Join(delivery, "report.py"), []byte("DEFAULT_LIMIT = 8\n"), 0o600))
	testutil.FailErr(t, "compare changed delivery", facts.ReadSources(t.Context(), capture, delivery))
	if facts.Snapshots[snapshot.ID].MatchesDelivered {
		t.Fatal("stale snapshot accepted")
	}
	testutil.FailErr(t, "restore delivery", os.WriteFile(filepath.Join(delivery, "report.py"), body, 0o600))
	testutil.FailErr(t, "add unchecked file", os.WriteFile(filepath.Join(delivery, "test_new.py"), []byte("pass\n"), 0o600))
	testutil.FailErr(t, "compare expanded delivery", facts.ReadSources(t.Context(), capture, delivery))
	if facts.Snapshots[snapshot.ID].MatchesDelivered {
		t.Fatal("snapshot omitted delivered file")
	}
}

func TestDeliveryRejectsSymlinksAndUsesApplicationScope(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"report.py", "vendor/library.py", "src/vendor_adapter.py"} {
		path := filepath.Join(root, name)
		testutil.FailErr(t, "create parent", os.MkdirAll(filepath.Dir(path), 0o700))
		testutil.FailErr(t, "write file", os.WriteFile(path, []byte("pass\n"), 0o600))
	}
	files, err := deliveredFiles(t.Context(), root)
	testutil.FailErr(t, "read delivered identities", err)
	if len(files) != 2 {
		t.Fatalf("admitted paths: %+v", files)
	}
	testutil.FailErr(t, "create linked source", os.Symlink(filepath.Join(root, "report.py"), filepath.Join(root, "linked.py")))
	if _, err := deliveredFiles(t.Context(), root); err == nil {
		t.Fatal("linked delivery accepted")
	}
}

func TestGitIndexIdentityMatchesRetainedBytes(t *testing.T) {
	capture, root, delivery := t.TempDir(), t.TempDir(), t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(capture, "store.db"))
	snapshots := sourcesnapshot.New(database, sourceblob.New(filepath.Join(capture, "content")), filepath.Join(capture, "observations.db"),
		backgroundwork.New(map[backgroundwork.Resource]backgroundwork.Limits{backgroundwork.ResourceIO: {Total: 1}}))
	t.Cleanup(func() { testutil.FailErr(t, "close snapshots", snapshots.Close()) })
	body := []byte("LIMIT = 3\n")
	for _, dir := range []string{root, delivery} {
		testutil.FailErr(t, "write git source", os.WriteFile(filepath.Join(dir, "app.py"), body, 0o600))
	}
	gittest.InitCommit(t, root, "source")
	snapshot, err := snapshots.Ensure(t.Context(), sourcesnapshot.Request{Roots: []sourcesnapshot.Root{{Path: root}}})
	testutil.FailErr(t, "capture git index", err)
	var indexed int
	testutil.FailErr(t, "count index identities", database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM source_snapshot_chunks c JOIN source_manifest_entries e ON e.chunk_id=c.chunk_id WHERE c.snapshot_id=? AND e.identity='index'`, snapshot.ID).Scan(&indexed))
	if indexed != 1 {
		t.Fatalf("index identities: got %d", indexed)
	}
	_, err = database.ExecContext(t.Context(), `PRAGMA wal_checkpoint(TRUNCATE)`)
	testutil.FailErr(t, "checkpoint git snapshot", err)
	facts := Evidence{}
	testutil.FailErr(t, "compare retained git files", facts.ReadSources(t.Context(), capture, delivery))
	if !facts.Snapshots[snapshot.ID].MatchesDelivered {
		t.Fatal("equal git blob rejected")
	}
	testutil.FailErr(t, "change delivered git file", os.WriteFile(filepath.Join(delivery, "app.py"), []byte("LIMIT = 8\n"), 0o600))
	testutil.FailErr(t, "compare changed git files", facts.ReadSources(t.Context(), capture, delivery))
	if facts.Snapshots[snapshot.ID].MatchesDelivered {
		t.Fatal("different git blob accepted")
	}
}

func TestDeliveredFilesRejectsExternalLinks(t *testing.T) {
	for _, directory := range []bool{false, true} {
		name := "file"
		if directory {
			name = "directory"
		}
		t.Run(name, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			payload := filepath.Join(outside, "private.py")
			testutil.FailErr(t, "write external payload", os.WriteFile(payload, []byte("private = True\n"), 0o600))
			target := payload
			if directory {
				target = outside
			}
			testutil.FailErr(t, "link external payload", os.Symlink(target, filepath.Join(root, "linked.py")))
			if _, err := deliveredFiles(t.Context(), root); err == nil {
				t.Fatal("external link accepted as delivered source")
			}
		})
	}
}
