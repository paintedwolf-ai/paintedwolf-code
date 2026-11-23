package anchor_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/processfixture"
)

func TestLoadRegistryRejectsUnknownOn(t *testing.T) {
	testutil.FailErr(t, "install catalog", anchorcatalog.InstallBundled())

	// Invalid overlay bindings fail at load.
	body := "'on': not.a.catalog.anchor\neffect: inform\nrender: bogus\ntier: builtin\n"
	_, err := anchor.LoadRegistryFromConfigRootWithCatalog(bindingCatalog("bad", []byte(body)))
	if err == nil || !strings.Contains(err.Error(), "unknown id") {
		t.Fatalf("want unknown id reject, got %v", err)
	}
}

func TestInstallCatalogRequiredForParseID(t *testing.T) {
	if !processfixture.Startup(t, nil) {
		return
	}
	if _, ok := anchor.ParseID("leg.finished"); ok {
		t.Fatal("ParseID must fail closed without catalog")
	}
	testutil.FailErr(t, "install", anchorcatalog.InstallBundled())
	id, ok := anchor.ParseID("leg.finished")
	if !ok || id != anchor.LegFinished {
		t.Fatalf("got %q ok=%v", id, ok)
	}
}
