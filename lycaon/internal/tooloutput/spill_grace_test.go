package tooloutput_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

func TestRenewSpillGraceTouchesOnlyToolOutputBodies(t *testing.T) {
	host := t.TempDir()
	aged := time.Now().Add(-time.Hour)
	for rel, renewed := range map[string]bool{
		tooloutput.ToolOutputSpillDir + "/a.txt": true,
		tooloutput.PromoteSpillDir + "/a.json":   false,
	} {
		abs := tooloutput.DiskPath(host, rel)
		testutil.FailErr(t, "dir", os.MkdirAll(filepath.Dir(abs), 0o700))
		testutil.FailErr(t, "body", os.WriteFile(abs, []byte("x"), 0o600))
		testutil.FailErr(t, "age", os.Chtimes(abs, aged, aged))
		testutil.FailErr(t, "renew", tooloutput.RenewSpillGrace(host, rel, time.Now()))
		info, err := os.Stat(abs)
		testutil.FailErr(t, "stat", err)
		if got := info.ModTime().After(aged.Add(time.Minute)); got != renewed {
			t.Fatalf("%s renewed=%v want %v", rel, got, renewed)
		}
	}
	if err := tooloutput.RenewSpillGrace(host, tooloutput.ToolOutputSpillDir+"/missing.txt", time.Now()); !os.IsNotExist(err) {
		t.Fatalf("missing body err=%v", err)
	}
}
