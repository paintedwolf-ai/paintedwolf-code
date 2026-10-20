package bundleddriver

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestOpenGrepTargetsDoNotExposeExcludedAncestorNames(t *testing.T) {
	root := filepath.Join(t.TempDir(), "build", "project")
	projection := filepath.Join(filepath.Dir(root), "private-output", "embedded")
	paths := []string{root, filepath.Join(root, "src", "main.rs"), filepath.Join("src", "main.rs"), projection}
	got, err := opengrepRelativeTargets(root, paths)
	testutil.FailErr(t, "scope scanner arguments to its project working directory", err)
	want := []string{".", filepath.Join("src", "main.rs"), filepath.Join("src", "main.rs"), filepath.Join("..", "private-output", "embedded")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scanner target scope = %#v, want %#v", got, want)
	}
	if paths[0] != root || paths[3] != projection {
		t.Fatal("scanner invocation changed its caller's authorized target identities")
	}
}
