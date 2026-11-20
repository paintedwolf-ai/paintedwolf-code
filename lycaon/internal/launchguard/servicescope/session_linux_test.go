package servicescope

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func TestServiceLimitsAndDelegator(t *testing.T) {
	fixture := t.TempDir()
	if err := setLimits(fixture, 1); err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := openDirectory(int(root.Fd()), "missing/child", false, 0); err == nil {
		t.Fatal("missing parent accepted")
	}
	if err := os.Symlink(fixture, filepath.Join(fixture, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := openObject(int(root.Fd()), "link", unix.O_DIRECTORY); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := writeControl(int(root.Fd()), "pids.max", "128"); err != nil {
		t.Fatal(err)
	}

}
