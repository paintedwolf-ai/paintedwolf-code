package localdatatest

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// SeedProtected returns every marker that must survive a local-data clear.
func SeedProtected(t testing.TB, base string) []string {
	t.Helper()
	var paths []string
	for _, entry := range Entries() {
		if !entry.Preserve {
			continue
		}
		path := filepath.Join(base, entry.Name)
		if entry.IsDir {
			path = filepath.Join(path, "durable-marker.json")
		}
		testutil.FailErr(t, "create protected fixture directory", os.MkdirAll(filepath.Dir(path), 0o700))
		testutil.FailErr(t, "write protected fixture", os.WriteFile(path, []byte("keep\n"), 0o600))
		paths = append(paths, path)
	}
	return paths
}

func SecretFiles() []string {
	var names []string
	for _, entry := range Entries() {
		if entry.IsSecret && !entry.IsDir {
			names = append(names, entry.Name)
		}
	}
	sort.Strings(names)
	return names
}
