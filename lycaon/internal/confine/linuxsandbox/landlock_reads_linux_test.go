//go:build linux

package linuxsandbox

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReadRulesHoldAnAbsentDeniedPathByItsWay(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{".bashrc", "projects", ".config"} {
		testutil.FailErr(t, "fixture "+name, os.Mkdir(filepath.Join(home, name), 0o700))
	}
	testutil.FailErr(t, "fixture link", os.Symlink("projects", filepath.Join(home, "link")))
	absent := filepath.Join(home, ".config", "gcloud")
	rules, err := readRules(Filesystem{Hidden: []string{absent, filepath.Join(home, "present")}})
	testutil.FailErr(t, "read rules", err)
	byPath := map[string]uint64{}
	for _, r := range rules {
		byPath[r.path] = r.access
	}
	for _, dir := range []string{"/", home, filepath.Join(home, ".config")} {
		if byPath[dir] != unix.LANDLOCK_ACCESS_FS_READ_DIR {
			t.Fatalf("%s on the way grants %#x, want listing only", dir, byPath[dir])
		}
	}
	for _, beside := range []string{filepath.Join(home, ".bashrc"), filepath.Join(home, "projects"), "/usr"} {
		if byPath[beside] != landlockReadExecute {
			t.Fatalf("%s beside the way grants %#x, want read and execute", beside, byPath[beside])
		}
	}
	if _, ok := byPath[absent]; ok {
		t.Fatal("the absent denied path was granted")
	}
	if _, ok := byPath[filepath.Join(home, "link")]; ok {
		t.Fatal("a symlink was ruled; its target is ruled where it lives")
	}
}

func TestReadRulesKeepTheWholeTreeWhenEveryDeniedPathExists(t *testing.T) {
	present := t.TempDir()
	rules, err := readRules(Filesystem{Hidden: []string{present}})
	testutil.FailErr(t, "read rules", err)
	if len(rules) != 1 || rules[0].path != "/" || rules[0].access != landlockReadExecute {
		t.Fatalf("rules = %+v, want read and execute on /", rules)
	}
	// A denied path inside a write root keeps that root's grant.
	root := t.TempDir()
	rules, err = readRules(Filesystem{Hidden: []string{filepath.Join(root, "absent")}, WriteRoots: []string{root}})
	testutil.FailErr(t, "read rules", err)
	if len(rules) != 1 || rules[0].path != "/" {
		t.Fatalf("rules = %+v, want only /", rules)
	}
}
