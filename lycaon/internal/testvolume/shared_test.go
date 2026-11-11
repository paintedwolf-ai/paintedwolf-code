//go:build darwin

package testvolume

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSharedVolumeScopesDirectoriesPerTest(t *testing.T) {
	volume, ran := Shared(t, Clonable)
	if ran {
		return
	}
	dir := volume.Dir(t, "project")
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "kept.txt"), []byte("kept"), 0o600))
	if !strings.HasPrefix(dir, volume.Root+string(filepath.Separator)) {
		t.Fatalf("directory left the volume: %s", dir)
	}
	if filepath.Dir(dir) == volume.Root {
		t.Fatalf("directory is not scoped to the test: %s", dir)
	}
	other, ran := Shared(t, Movable)
	if ran {
		return
	}
	if other.Root != volume.Root {
		t.Fatalf("shared volume changed: %s, want %s", other.Root, volume.Root)
	}
	if other.Dir(t, "project") == dir {
		t.Fatal("two tests share a scope")
	}
}
