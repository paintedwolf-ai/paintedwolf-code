//go:build windows

package fseffect_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWindowsOwnershipIsAnExplicitUnavailableCapability(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "untouched")
	testutil.FailErr(t, "seed metadata target", os.WriteFile(path, []byte("fixture"), 0600))
	result, err := fseffect.ChangeOwnership(fseffect.OwnershipRequest{
		Location: fseffect.Location{Root: root, Rel: "untouched"}, UID: 12345, GID: 12345,
	})
	if !errors.Is(err, fseffect.ErrUnsupported) || result.Applied || result.Capability != fseffect.MetadataUnsupported {
		t.Fatalf("ownership outcome=%+v error=%v", result, err)
	}
	got, err := os.ReadFile(path)
	testutil.FailErr(t, "read unchanged target", err)
	if string(got) != "fixture" {
		t.Fatalf("unsupported metadata changed bytes: %q", got)
	}
}
