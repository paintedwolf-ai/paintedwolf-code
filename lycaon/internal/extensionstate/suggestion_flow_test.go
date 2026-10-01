package extensionstate_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
)

func TestAcceptSuggestionIsDeviceInstall(t *testing.T) {
	owner := testOwner(t)
	fixture := filepath.Join(t.TempDir(), "acme-suggest")
	extpackstest.WriteMinimalPack(t, fixture, "acme/suggest-pack", 1)

	res := apply(t, owner, deviceScope(), extensionstate.InstallOp{Source: "path:" + fixture})
	if res.Install == nil || res.Install.PackID != "acme/suggest-pack" {
		t.Fatalf("install = %+v", res.Install)
	}
	apply(t, owner, deviceScope(), extensionstate.SetInstalledFromOp{
		PackID: "acme/suggest-pack", ProjectID: "proj-from-repo",
	})

	path, err := extpacks.DeviceDesiredPath()
	testutil.FailErr(t, "device desired path", err)
	desired, err := extpacks.LoadDesiredFile(path)
	testutil.FailErr(t, "load device desired", err)
	row, ok := extpacks.DesiredPackRow(desired, "acme/suggest-pack")
	if !ok || row.InstalledFrom != "proj-from-repo" {
		t.Fatalf("device pack row = %+v present=%v", row, ok)
	}
	lockPath, err := extpacks.DeviceLockPath()
	testutil.FailErr(t, "device lock path", err)
	lock, err := extpacks.LoadLockFile(lockPath)
	testutil.FailErr(t, "load device lock", err)
	if _, ok := lock.Package("acme/suggest-pack"); !ok {
		t.Fatal("accept must publish the pack on the device lock")
	}
}

func TestAcceptRequiresExpectedRevision(t *testing.T) {
	owner := testOwner(t)
	fixture := filepath.Join(t.TempDir(), "acme-rev")
	extpackstest.WriteMinimalPack(t, fixture, "acme/rev-pack", 1)
	_, err := owner.Apply(t.Context(), extensionstate.Intent{
		Scope: deviceScope(),
		Op:    extensionstate.InstallOp{Source: "path:" + fixture},
	})
	if !errors.Is(err, extensionstate.ErrExpectedRevisionRequired) {
		t.Fatalf("err = %v, want ErrExpectedRevisionRequired", err)
	}
}
