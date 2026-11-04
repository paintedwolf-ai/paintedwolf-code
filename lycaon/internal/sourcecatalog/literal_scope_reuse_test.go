package sourcecatalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestContentFactsFollowFilesAcrossSearchScopes(t *testing.T) {
	root := t.TempDir()
	paths := []string{"src/a.go", "src/b.go", "src/deep/c.go", "docs/a.md", "docs/b.md", "other/c.txt"}
	catalog := New()
	catalog.Literals.cache.versionOf = fixtureFileVersion
	for _, path := range paths {
		writeLiteralFixture(t, root, path, "shared current content\n")
	}
	learn := func(scope string) int {
		t.Helper()
		learned := 0
		for _, path := range paths {
			if scope != "" && !strings.HasPrefix(path, scope+"/") {
				continue
			}
			info, err := os.Stat(filepath.Join(root, filepath.FromSlash(path)))
			testutil.FailErr(t, "stat scoped source", err)
			entry := Entry{Path: path, Size: info.Size(), Mode: uint32(info.Mode()), Modified: info.ModTime()}
			version := catalog.Literals.cache.versionOf(root, entry)
			if read := catalog.Literals.BeginRead(root, entry, version); read != nil {
				content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
				testutil.FailErr(t, "read scoped source", err)
				if !captureObserved(read, t.Context(), content) {
					t.Fatalf("did not learn %s", path)
				}
				learned++
			}
			for _, require := range []litprefilter.Requirement{litprefilter.AnyOf("unrelated-absent-token"), litprefilter.AllOf("other-missing-value", "never-present")} {
				if !catalog.Literals.CanPruneBytes(root, require, entry, version) || !catalog.Literals.CanPrune(root, require, entry) {
					t.Fatalf("current facts unavailable for %s", path)
				}
			}
			if catalog.Literals.CanPruneBytes(root, litprefilter.AnyOf("current"), entry, version) {
				t.Fatalf("current match excluded for %s", path)
			}
		}
		return learned
	}
	for _, stage := range []struct {
		scope   string
		learned int
	}{{"src", 3}, {"src/deep", 0}, {"docs", 2}, {"", 1}, {"docs", 0}, {"", 0}} {
		if got := learn(stage.scope); got != stage.learned {
			t.Fatalf("scope %q learned %d files, want %d", stage.scope, got, stage.learned)
		}
	}
	changed := paths[1]
	writeLiteralFixture(t, root, changed, "shared current changed content\n")
	catalog.InvalidateRoot(root, changed)
	if got := learn(""); got != 1 {
		t.Fatalf("path-local change relearned %d files, want 1", got)
	}
	if got := learn(""); got != 0 {
		t.Fatalf("stable broad search relearned %d files", got)
	}
	for _, path := range []string{"docs", "src/deep"} {
		catalog.InvalidateRoot(root, path)
		want := 1
		if path == "docs" {
			want = 2
		}
		if got := learn(""); got != want {
			t.Fatalf("%s change relearned %d files, want %d", path, got, want)
		}
	}
	catalog.InvalidateRoot(root)
	if got := learn(""); got != len(paths) {
		t.Fatalf("unknown change relearned %d files, want %d", got, len(paths))
	}
}
