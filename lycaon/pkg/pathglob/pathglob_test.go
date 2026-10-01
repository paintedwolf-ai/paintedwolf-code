package pathglob_test

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/pathglob"
)

func TestMatchRecursiveAndWholeRoot(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"internal/**", "internal/ntp/client.go", true},
		{"internal/**", "internal/ntp/testdata/reply.bin", true},
		{"src/**/*.go", "src/a.go", true},
		{"src/**/*.go", "src/deep/a.go", true},
		{"src/*.go", "src/deep/a.go", false},
	}
	for _, tc := range cases {
		if got := pathglob.Match(tc.pattern, tc.path); got != tc.want {
			t.Fatalf("Match(%q, %q) = %v want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestCoversDirectoryAndGlob(t *testing.T) {
	if !pathglob.Covers("internal", "internal/ntp/client.go") {
		t.Fatal("directory scope must cover descendants")
	}
	if !pathglob.Covers("internal/**", "internal/ntp/client.go") {
		t.Fatal("recursive glob must cover descendants")
	}
	if pathglob.Covers("internal/*.go", "internal/ntp/client.go") {
		t.Fatal("single-star glob must not cross a path segment")
	}
	if !pathglob.Covers(".", "src/deep/a.go") {
		t.Fatal("whole-root scope must cover every relative path")
	}
}
