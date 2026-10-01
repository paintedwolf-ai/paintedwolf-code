package sandbox

import (
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/pathglob"
)

func TestPathGlobMatchTrailingDoubleStarRequiresPrefix(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		path    string
		want    bool
	}{
		{"**/" + settingsoverlay.DirName() + "/store.db/**", "README.md", false},
		{"**/sidecar.log/**", "lycaon/internal/foo.go", false},
		{".git/**", "README.md", false},
		{".git/**", ".git/config", true},
		{"lycaon/**", "lycaon", false},
		{"lycaon/**", "lycaon/internal/tools.go", true},
		{"**/tests/**", "tests/test_foo.py", true},
		{"**/tests/**", "pkg/tests/foo.py", true},
		{"**/tests/**", "test_foo.py", false},
	} {
		if got := pathglob.Match(tc.pattern, tc.path); got != tc.want {
			t.Fatalf("pathglob.Match(%q, %q) = %v want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestEntryGlobBasenameVsPath(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		path    string
		want    bool
	}{
		{"*.go", "lycaon/internal/tools.go", true},
		{"*.go", "notes.txt", false},
		{"go.mod", "lycaon/go.mod", true},
		{"**/*.go", "lycaon/internal/tools.go", true},
		{"*.go", "lycaon/internal/tools.go", true},
		{"internal/*.go", "lycaon/internal/tools.go", false},
		{"lycaon/internal/*.go", "lycaon/internal/tools.go", true},
	} {
		glob, err := CompileEntryGlob(tc.pattern)
		testutil.FailErr(t, "compile entry glob", err)
		if got := glob.Match(tc.path); got != tc.want {
			t.Fatalf("glob %q matches %q = %v want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestPathGlobMatchDeepStarFilePattern(t *testing.T) {
	if !pathglob.Match("**/"+settingsoverlay.DirName()+"/store.db", "lycaon/"+settingsoverlay.DirName()+"/store.db") {
		t.Fatal("expected nested store.db match")
	}
	if pathglob.Match("**/"+settingsoverlay.DirName()+"/store.db", "README.md") {
		t.Fatal("README.md must not match store.db pattern")
	}
}
