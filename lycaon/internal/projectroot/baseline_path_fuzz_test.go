package projectroot

import (
	"path/filepath"
	"strings"
	"testing"
)

// Every root's baseline spelling parses back to that root and path, so no
// second spelling can resolve to it. The grammar cannot spell an unlabeled
// secondary root or a primary path whose first segment starts with "@"; both
// are outside this property.
func FuzzBaselinePathRoundTrip(f *testing.F) {
	for _, seed := range []struct {
		rel   string
		which uint8
	}{{"main.go", 0}, {"plan.md", 1}, {".", 1}, {"root-abc/x", 0}, {"a/b/../c", 0}, {"nested/dir/file.txt", 1}} {
		f.Add(seed.rel, seed.which)
	}
	roots := baselineParseRoots()[:2]
	f.Fuzz(func(t *testing.T, rel string, which uint8) {
		root := roots[int(which)%len(roots)]
		clean := filepath.ToSlash(filepath.Clean(rel))
		if strings.TrimSpace(rel) != rel || rel == "" || !baselineRelSafe(clean) || strings.HasPrefix(clean, "@") {
			t.Skip()
		}
		display := BaselineDisplayPath(roots, root.ID, clean)
		got, gotRel, ok := ParseBaselinePath(roots, display)
		if !ok || got.ID != root.ID || gotRel != clean {
			t.Fatalf("%s %q displays as %q and parses to %q %q (ok=%v)", root.ID, clean, display, got.ID, gotRel, ok)
		}
	})
}
