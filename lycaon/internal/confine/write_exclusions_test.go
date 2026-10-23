package confine

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWriteExclusionsOverrideRootGrantsAndPreserveReads(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "draft.txt")
	testutil.FailErr(t, "seed file", os.WriteFile(path, []byte("saved\n"), 0o644))
	c := Confinement{Roots: []string{root}, GrantedWriteRoots: []string{root}, WriteDenyPaths: []string{path}}
	rules, err := filesystemRules(c)
	if runtime.GOOS == "linux" {
		var unavailable *WriteExclusionUnavailableError
		if !errors.As(err, &unavailable) || unavailable.Path != path || unavailable.Reason != exclusionAncestorRemovalUnsupported {
			t.Fatalf("unsupported protection admitted: %v", err)
		}
		return
	}
	testutil.FailErr(t, "filesystem rules", err)
	if verdict := rules.Verdict(AccessWrite, path); verdict.Allowed || verdict.Layer != FloorWriteConfigured || !verdict.Layer.Recovery().Terminal() {
		t.Fatalf("protected verdict=%+v", verdict)
	}
	if !rules.Verdict(AccessRead, path).Allowed || !rules.Verdict(AccessWrite, filepath.Join(root, "sibling.txt")).Allowed {
		t.Fatal("exclusion blocked reads or sibling writes")
	}
	profile, err := BuildSeatbeltProfile(c)
	testutil.FailErr(t, "profile", err)
	if !strings.Contains(profile, "(deny file-write-unlink") || !strings.Contains(profile, filepath.Base(path)) {
		t.Fatalf("profile lacks immutable namespace protection: %s", profile)
	}
}

func TestWriteExclusionsRefuseUnresolvablePathIdentity(t *testing.T) {
	root := t.TempDir()
	loop := filepath.Join(root, "loop")
	testutil.FailErr(t, "symlink loop", os.Symlink(loop, loop))
	for _, path := range []string{loop, "relative.txt", ""} {
		_, err := filesystemRules(Confinement{Roots: []string{root}, WriteDenyPaths: []string{path}})
		var unavailable *WriteExclusionUnavailableError
		if !errors.As(err, &unavailable) || unavailable.Path != path || unavailable.Reason != "path_resolution_unavailable" {
			t.Fatalf("path=%q error=%v", path, err)
		}
	}
}
