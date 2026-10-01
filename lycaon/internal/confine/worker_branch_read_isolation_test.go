package confine_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSeatbeltWorkerBranchCannotReadPrimarySource(t *testing.T) {
	self := requireSeatbelt(t)
	primary := t.TempDir()
	branch := t.TempDir()
	primaryFile := filepath.Join(primary, "src", "module.py")
	branchFile := filepath.Join(branch, "src", "module.py")
	for _, file := range []string{primaryFile, branchFile} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(file), 0o700))
		testutil.FailErr(t, "write", os.WriteFile(file, []byte("value = 1\n"), 0o600))
	}

	boundary := confine.Confinement{
		Roots: []string{branch}, ReadDenyPaths: []string{primary}, ReadRoots: []string{branch},
	}
	if code, out := confinedRun(t, self, boundary, "/bin/cat", branchFile); code != 0 {
		t.Fatalf("worker branch must be readable, exit=%d out=%s", code, out)
	}
	if code, out := confinedRun(t, self, boundary, "/bin/cat", primaryFile); code == 0 {
		t.Fatalf("primary source must stay read-denied, out=%s", out)
	}
}
