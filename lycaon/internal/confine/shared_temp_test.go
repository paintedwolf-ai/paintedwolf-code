package confine

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
)

func TestSharedTempGrantNamesTheLocationAtEveryDepth(t *testing.T) {
	location := t.TempDir()
	t.Setenv("TMPDIR", location)
	root := fspath.CanonicalPath(location)
	rules, err := filesystemRules(Confinement{})
	if err != nil {
		t.Fatal(err)
	}
	path := root
	for depth := 1; depth <= 6; depth++ {
		path = filepath.Join(path, "build")
		verdict := rules.Verdict(AccessWrite, path)
		if verdict.Layer != FloorSharedTemp || verdict.Layer.Recovery().GrantPath(path) != root {
			t.Fatalf("depth %d: %+v", depth, verdict)
		}
	}
	rules, err = filesystemRules(Confinement{GrantedWriteRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	if !rules.Verdict(AccessWrite, path).Allowed {
		t.Fatal("reviewed temp root did not admit nested write")
	}
}
