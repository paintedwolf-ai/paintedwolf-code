// Package testbaseline creates worker baseline fixtures through the capture path.
package testbaseline

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

func Capture(t testing.TB, dir string) string {
	t.Helper()
	root := t.TempDir()
	store := workspacebaseline.New(nil, sourceblob.New(filepath.Join(root, "source-content")), filepath.Join(root, "manifests"))
	path, err := store.Capture(t.Context(), "fixture", workspacebaseline.Branch(nil, dir))
	testutil.FailErr(t, "capture workspace baseline", err)
	return path
}

type File struct {
	MtimeNano int64
	Content   string
}

func FromFiles(t testing.TB, files map[string]File) string {
	t.Helper()
	root := t.TempDir()
	for path, file := range files {
		abs := filepath.Join(root, filepath.FromSlash(path))
		testutil.FailErr(t, "create baseline fixture directory", os.MkdirAll(filepath.Dir(abs), 0o700))
		content := file.Content
		testutil.FailErr(t, "write baseline fixture", os.WriteFile(abs, []byte(content), 0o600))
		if file.MtimeNano != 0 {
			timestamp := time.Unix(0, file.MtimeNano)
			testutil.FailErr(t, "set baseline fixture time", os.Chtimes(abs, timestamp, timestamp))
		}
	}
	return Capture(t, root)
}

func Durable(t testing.TB, database db.Handle, jobID, dir string) string {
	t.Helper()
	root, err := db.Directory(t.Context(), database)
	testutil.FailErr(t, "resolve durable data directory", err)
	store := workspacebaseline.New(database, sourceblob.New(filepath.Join(root, "source-content")), filepath.Join(root, "worker-baselines"))
	path, err := store.Capture(t.Context(), jobID, workspacebaseline.Branch(nil, dir))
	testutil.FailErr(t, "capture durable baseline", err)
	return path
}

// DataDir resolves a fixture's installation root from its active database.
func DataDir(t testing.TB, database db.DBTX) string {
	t.Helper()
	root, err := db.Directory(t.Context(), database)
	testutil.FailErr(t, "resolve fixture installation", err)
	return root
}
