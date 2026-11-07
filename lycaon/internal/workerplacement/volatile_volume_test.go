//go:build linux

package workerplacement_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testvolume"
	"github.com/lycaon/lycaon/internal/workerplacement"
)

func TestTmpfsCannotHoldTheLastResortFolder(t *testing.T) {
	volume, ran := testvolume.New(t, testvolume.NoClone)
	if ran {
		return
	}
	if !workerplacement.FilesystemIsVolatile(volume.Root) {
		t.Fatalf("%s is tmpfs", volume.Root)
	}
	if workerplacement.FilesystemIsVolatile(t.TempDir()) {
		t.Skip("the temporary directory is itself tmpfs here")
	}
}
