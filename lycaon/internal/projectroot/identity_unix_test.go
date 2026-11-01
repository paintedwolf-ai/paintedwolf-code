//go:build !windows

package projectroot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRootAliasesShareIdentityWithoutAdmittingEscapes(t *testing.T) {
	base := t.TempDir()
	root, outside, alias := filepath.Join(base, "root "), filepath.Join(base, "outside"), filepath.Join(base, "alias ")
	for _, path := range []string{root, outside} {
		testutil.FailErr(t, "create root", os.Mkdir(path, 0o700))
	}
	testutil.FailErr(t, "link root alias", os.Symlink(root, alias))
	testutil.FailErr(t, "link escaping child", os.Symlink(outside, filepath.Join(root, "escape")))
	for _, rootPath := range []string{root, alias} {
		for _, candidateRoot := range []string{root, alias} {
			for _, tail := range []string{"new/leaf", "escape/new/leaf"} {
				candidate := filepath.Join(candidateRoot, filepath.FromSlash(tail))
				want := tail == "new/leaf"
				if got := PathWithinRoot(rootPath, candidate); got != want {
					t.Fatalf("containment of %q in %q = %t, want %t", candidate, rootPath, got, want)
				}
				matched, rel, ok := MatchRoot([]RootRef{{ID: "root", Path: rootPath}}, candidate)
				if ok != want || ok && (matched.ID != "root" || rel != "new/leaf") {
					t.Fatalf("root match of %q = %+v %q %t", candidate, matched, rel, ok)
				}
			}
		}
	}
}
