package sourcescope

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestIgnoreCacheSeparatesNestedRootDomains(t *testing.T) {
	for _, nestedFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "outer first", true: "nested first"}[nestedFirst], func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, "module/.gitignore", "generated/\n")
			outer := func() bool {
				return New(root, Options{Plane: capturePlane()}).AdmitPath("module/generated/file.go", false)
			}
			nested := func() bool {
				return New(filepath.Join(root, "module"), Options{Plane: capturePlane()}).AdmitPath("generated/file.go", false)
			}
			first, second := outer, nested
			if nestedFirst {
				first, second = nested, outer
			}
			if first() || second() {
				t.Fatal("cached ignore patterns lost their domain when the attached root changed")
			}
		})
	}
}

func TestIgnoreCacheRefreshesAfterSameMetadataExternalEdit(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "first/\n")
	file := filepath.Join(root, ".gitignore")
	info, err := os.Stat(file)
	testutil.FailErr(t, "stat ignore file", err)
	if New(root, Options{Plane: capturePlane()}).AdmitPath("first/file.go", false) {
		t.Fatal("initial ignore pattern was not applied")
	}
	writeFile(t, root, ".gitignore", "other/\n")
	testutil.FailErr(t, "preserve external timestamp", os.Chtimes(file, info.ModTime(), info.ModTime()))
	current := New(root, Options{Plane: capturePlane()})
	if !current.AdmitPath("first/file.go", false) || current.AdmitPath("other/file.go", false) {
		t.Fatal("external ignore edit reused stale patterns despite changed bytes")
	}
}
