//go:build !windows

package editoroutbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestValidateRefusesTemporarySymlinksWithoutTouchingTheirTargets(t *testing.T) {
	root := t.TempDir()
	envelope := seedEnvelope(t, root)
	outside := filepath.Join(t.TempDir(), "retained.txt")
	testutil.FailErr(t, "write outside target", os.WriteFile(outside, []byte("original"), 0o600))
	testutil.FailErr(t, "inject temporary symlink", os.Symlink(outside, envelope.header+".tmp"))
	if err := Validate(t.Context(), root); err == nil {
		t.Fatal("restore accepted a temporary file that redirects native writes")
	}
	retained, err := os.ReadFile(outside)
	testutil.FailErr(t, "read outside target", err)
	if string(retained) != "original" {
		t.Fatal("validation modified the symlink target")
	}
	info, err := os.Lstat(envelope.header + ".tmp")
	testutil.FailErr(t, "read refused symlink", err)
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("validation replaced the refused link")
	}
}

func TestValidateRefusesAnOutboxRootSymlink(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	seedEnvelope(t, other)
	testutil.FailErr(t, "redirect native root", os.Symlink(filepath.Join(other, Directory()), filepath.Join(root, Directory())))
	if err := Validate(t.Context(), root); err == nil {
		t.Fatal("restore accepted an outbox root outside the installation")
	}
	testutil.FailErr(t, "verify original remains valid", Validate(t.Context(), other))
}
