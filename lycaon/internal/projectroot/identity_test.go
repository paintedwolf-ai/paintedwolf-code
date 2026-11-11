package projectroot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRootIdentityPreservesOperandWhitespaceAndMissingTails(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, " workspace")
	sibling := filepath.Join(base, "workspace")
	for _, path := range []string{root, sibling} {
		testutil.FailErr(t, "create operand root", os.Mkdir(path, 0o700))
	}
	t.Chdir(base)
	for _, candidate := range []string{filepath.Join(root, "new", "leaf"), filepath.Join(" workspace", "new", "leaf")} {
		if !PathWithinRoot(root, candidate) {
			t.Fatalf("missing tail lost its actual root: %q", candidate)
		}
		matched, rel, ok := MatchRoot([]RootRef{{ID: "actual", Path: root}, {ID: "sibling", Path: sibling}}, candidate)
		if !ok || matched.ID != "actual" || rel != "new/leaf" {
			t.Fatalf("root identity = %+v %q %t", matched, rel, ok)
		}
	}
	if PathWithinRoot(root, filepath.Join(sibling, "new", "leaf")) || PathWithinRoot("", root) || PathWithinRoot(root, "") {
		t.Fatal("an empty or whitespace-trimmed operand acquired root authority")
	}
}
