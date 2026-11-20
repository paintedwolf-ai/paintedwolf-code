package servicescope

import (
	"github.com/lycaon/lycaon/internal/testutil"

	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func TestServiceLimitsAndDelegator(t *testing.T) {
	fixture := t.TempDir()
	testutil.FailErr(t, "set service process limits", setLimits(fixture, 1))
	for file, want := range map[string]string{"memory.max": "536870912", "pids.max": "256"} {
		got, err := os.ReadFile(filepath.Join(fixture, file))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v", file, got, err)
		}
	}
	if err := DelegateForBroker(0); err == nil {
		t.Fatal("root admitted as a service user")
	}
	root, err := os.Open(fixture)
	testutil.FailErr(t, "open cgroup fixture", err)
	defer root.Close()
	if _, err := openDirectory(int(root.Fd()), "missing/child", false, 0); err == nil {
		t.Fatal("missing parent accepted")
	}
	testutil.FailErr(t, "create cgroup symlink", os.Symlink(fixture, filepath.Join(fixture, "link")))
	if _, err := openObject(int(root.Fd()), "link", unix.O_DIRECTORY); err == nil {
		t.Fatal("symlink accepted")
	}
	testutil.FailErr(t, "write cgroup PID limit", writeControl(int(root.Fd()), "pids.max", "128"))

}
