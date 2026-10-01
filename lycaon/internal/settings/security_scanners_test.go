package settings_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSecurityScannersStoreGlobalOverlay(t *testing.T) {
	global := filepath.Join(t.TempDir(), "global-security-scanners.yaml")

	store, err := settings.NewSecurityScannersStoreAt(global)
	testutil.FailErr(t, "NewSecurityScannersStoreAt", err)
	if !store.Effective().Enabled {
		t.Fatal("expected bundled enabled=true")
	}
	if store.Effective().LandedChangeScope != "path_scoped" {
		t.Fatalf("default LandedChangeScope = %q", store.Effective().LandedChangeScope)
	}

	scope := "full_root"
	overlay := settings.SecurityScannersUserOverlay{
		Enabled:           boolPtr(false),
		LandedChangeScope: &scope,
	}
	testutil.FailErr(t, "PutGlobal", store.PutGlobal(overlay))
	if store.Effective().Enabled {
		t.Fatal("expected global disabled")
	}
	if store.Effective().LandedChangeScope != "full_root" {
		t.Fatalf("overlay LandedChangeScope = %q", store.Effective().LandedChangeScope)
	}
}

func boolPtr(v bool) *bool { return &v }
