package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResolveAbsAliasesAndMissingTails(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	testutil.FailErr(t, "alias attached root", os.Symlink(root, alias))
	testutil.FailErr(t, "create existing parent", os.Mkdir(filepath.Join(root, "sub"), 0o700))
	testutil.FailErr(t, "seed existing file", os.WriteFile(filepath.Join(root, "sub", "file"), []byte("retained"), 0o600))
	boundary := testBoundary(t)
	for _, operand := range []string{"sub/file", "sub/new/leaf", filepath.Join(alias, "sub", "new", "leaf")} {
		t.Run(operand, func(t *testing.T) {
			resolved, err := boundary.ResolveAbs(alias, operand)
			testutil.FailErr(t, "resolve through attached alias", err)
			rel := operand
			if filepath.IsAbs(operand) {
				rel = filepath.Join("sub", "new", "leaf")
			}
			want := filepath.Join(filepath.FromSlash(fspath.CanonicalPath(root)), rel)
			if resolved != want {
				t.Fatalf("resolved %q, want attached identity %q", resolved, want)
			}
		})
	}
}

func TestResolveAbsRefusesUnresolvedExistingLinks(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "seed dangling link", os.Symlink("missing", filepath.Join(root, "dangling")))
	testutil.FailErr(t, "seed link cycle", os.Symlink("loop", filepath.Join(root, "loop")))
	testutil.FailErr(t, "seed non-directory", os.WriteFile(filepath.Join(root, "file"), []byte("retained"), 0o600))
	boundary := testBoundary(t)
	for _, operand := range []string{"dangling", "dangling/child", "loop", "loop/child", "file/child"} {
		for _, absolute := range []bool{false, true} {
			path := operand
			if absolute {
				path = filepath.Join(root, path)
			}
			if _, err := boundary.ResolveAbs(root, path); err == nil {
				t.Fatalf("unresolvable existing entry was admitted: %q", path)
			} else if (operand == "dangling" || operand == "dangling/child") && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("dangling link lost filesystem refusal: %v", err)
			}
		}
	}
	if _, err := boundary.ResolveAbs(filepath.Join(root, "absent-root"), "file"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing root lost filesystem refusal: %v", err)
	}
}

func TestResolveAbsDoesNotAdmitAnOutsideLeafAlias(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	inside := filepath.Join(root, "file")
	testutil.FailErr(t, "seed attached file", os.WriteFile(inside, []byte("retained"), 0o600))
	alias := filepath.Join(outside, "alias")
	testutil.FailErr(t, "alias from outside", os.Symlink(inside, alias))
	if _, err := testBoundary(t).ResolveAbs(root, alias); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("outside leaf alias crossed the root boundary: %v", err)
	}
}
