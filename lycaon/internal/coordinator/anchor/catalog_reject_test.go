package anchor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadRegistryRejectsUnknownOn(t *testing.T) {
	testutil.FailErr(t, "install catalog", anchorcatalog.InstallBundled())

	// Invalid overlay bindings fail at load.
	dir := t.TempDir()
	body := "'on': not.a.catalog.anchor\neffect: inform\nrender: bogus\ntier: builtin\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(body), 0o600))
	_, err := anchor.LoadRegistry(extpacks.OnDisk(dir), "")
	if err == nil || !strings.Contains(err.Error(), "unknown id") {
		t.Fatalf("want unknown id reject, got %v", err)
	}
}

func TestInstallCatalogRequiredForParseID(t *testing.T) {
	anchorcatalog.Clear()
	t.Cleanup(func() {
		_ = anchorcatalog.InstallBundled()
	})
	if _, ok := anchor.ParseID("leg.finished"); ok {
		t.Fatal("ParseID must fail closed without catalog")
	}
	testutil.FailErr(t, "install", anchorcatalog.InstallBundled())
	id, ok := anchor.ParseID("leg.finished")
	if !ok || id != anchor.LegFinished {
		t.Fatalf("got %q ok=%v", id, ok)
	}
}
