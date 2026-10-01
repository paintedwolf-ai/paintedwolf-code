package settings_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPowerStoreDefaultsOnAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "power.yaml")
	store, err := settings.OpenPowerStore(path)
	testutil.FailErr(t, "load default power settings", err)
	if !store.KeepAwakeWhileWorking() {
		t.Fatal("KeepAwakeWhileWorking defaulted off")
	}
	testutil.FailErr(t, "disable keep awake", store.PutKeepAwakeWhileWorking(false))
	loaded, err := settings.OpenPowerStore(path)
	testutil.FailErr(t, "reload power settings", err)
	if loaded.KeepAwakeWhileWorking() {
		t.Fatal("KeepAwakeWhileWorking did not persist off")
	}
}
