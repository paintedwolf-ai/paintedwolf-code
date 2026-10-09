package scantest

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/testutil"
)

// OSVExport packs the vendored OSV records under test/testdata/osv into the
// export cache layout the dependency scanner reads offline
// (osv-scalibr/<ecosystem>/all.zip) and returns its directory.
func OSVExport(t testing.TB) string {
	t.Helper()
	records := filepath.Join(configlayout.FindModuleRoot(), "test", "testdata", "osv")
	dir := t.TempDir()
	ecosystems, err := os.ReadDir(records)
	testutil.FailErr(t, "read OSV records", err)
	for _, ecosystem := range ecosystems {
		files, err := filepath.Glob(filepath.Join(records, ecosystem.Name(), "*.json"))
		testutil.FailErr(t, "list OSV records", err)
		export := filepath.Join(dir, "osv-scalibr", ecosystem.Name())
		testutil.FailErr(t, "create ecosystem dir", os.MkdirAll(export, 0o750))
		out, err := os.Create(filepath.Join(export, "all.zip"))
		testutil.FailErr(t, "create OSV export", err)
		archive := zip.NewWriter(out)
		for _, file := range files {
			body, err := os.ReadFile(file)
			testutil.FailErr(t, "read OSV record", err)
			entry, err := archive.Create(filepath.Base(file))
			testutil.FailErr(t, "add OSV record", err)
			_, err = entry.Write(body)
			testutil.FailErr(t, "write OSV record", err)
		}
		testutil.FailErr(t, "close OSV export", archive.Close())
		testutil.FailErr(t, "close OSV export file", out.Close())
	}
	return dir
}
