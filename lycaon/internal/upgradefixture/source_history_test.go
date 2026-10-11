package upgradefixture

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSourceHistoryFixtureReadsDurableBodiesWithoutOriginalWorkspace(t *testing.T) {
	dataDir, projectDir := t.TempDir(), t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(dataDir, "store.db"))
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, projectDir)
	testdbseed.InsertSession(t, database, "session", testdbseed.DefaultProjectID)
	evidence, err := SeedSourceHistory(t.Context(), database, dataDir, projectDir, testdbseed.DefaultProjectID, "session")
	testutil.FailErr(t, "seed source and checkpoint through owning stores", err)
	testutil.FailErr(t, "remove original disposable workspace", os.RemoveAll(projectDir))
	testutil.FailErr(t, "read durable history without original workspace", VerifySourceHistory(t.Context(), database, dataDir, testdbseed.DefaultProjectID, "session", evidence))
	readOnly, err := db.OpenReadOnly(t.Context(), filepath.Join(dataDir, "store.db"))
	testutil.FailErr(t, "open diagnostic read-only handle", err)
	defer func() { _ = readOnly.Close() }()
	testutil.FailErr(t, "verify through read-only diagnostic handle", VerifySourceHistory(t.Context(), readOnly, dataDir, testdbseed.DefaultProjectID, "session", evidence))
	var objects int
	testutil.FailErr(t, "count deduplicated source objects", database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM source_blob_objects`).Scan(&objects))
	if objects != 2 {
		t.Fatalf("source history and checkpoint did not share bytes: objects=%d", objects)
	}
	rel, err := sourceblob.RelPath(evidence.BodySHA256)
	testutil.FailErr(t, "locate retained fixture body", err)
	target := filepath.Join(dataDir, enginepaths.SourceContentDirName, rel)
	testutil.FailErr(t, "make corruption fixture writable", os.Chmod(target, 0o600))
	testutil.FailErr(t, "inject corrupt retained bytes", os.WriteFile(target, []byte("corrupt"), 0o600))
	if err := VerifySourceHistory(t.Context(), database, dataDir, testdbseed.DefaultProjectID, "session", evidence); err == nil {
		t.Fatal("corrupt retained content passed fixture verification")
	}
}
