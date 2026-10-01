package extpacks_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
)

func TestSessionLoadUsesEffectiveCatalog(t *testing.T) {
	root := configlayout.FindModuleRoot()
	defer extpacks.ClearActive()

	eff, err := extpackstest.Resolve(t.Context(), extpackstest.Disabled("painted-wolf/security"), nil)
	testutil.FailErr(t, "extpacks.effective catalog resolve failed", err)
	testutil.FailErr(t, "extpacks.BootError failed", eff.BootError())
	extpacks.SetActive(eff)

	if eff.HasLoaded(extpacks.PolicyUnitID("WRITE_SCOPE_DENIED")) {
		t.Fatal("WRITE_SCOPE_DENIED must not keep when security disabled")
	}
	entries, err := hintregistry.ListEffective()
	testutil.FailErr(t, "hintregistry.ListEffective failed", err)
	for _, ent := range entries {
		if ent.Code == "WRITE_SCOPE_DENIED" {
			t.Fatal("ListStock must omit WRITE_SCOPE_DENIED")
		}
	}
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "guidance.LoadHintConfigStock failed", err)
	if _, ok := cfg.HintCodes["WRITE_SCOPE_DENIED"]; ok {
		t.Fatal("hint config must omit security OAR")
	}

	schemaDir := filepath.Join(filepath.Dir(root), "schemas")
	if _, err := os.Stat(filepath.Join(schemaDir, "oar", "oar.schema.json")); err != nil {
		t.Fatalf("oar schema: %v", err)
	}
	catalogPath := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml")
	if err := anchorcatalog.InstallFile(catalogPath); err != nil {
		testutil.FailErr(t, "anchorcatalog.InstallFile failed", err)
	}
	loader, err := oar.NewLoader(schemaDir)
	testutil.FailErr(t, "oar.NewLoader failed", err)
	rs, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "loader.LoadEffectivePolicy failed", err)
	for _, r := range rs.All() {
		if r != nil && r.ID == "WRITE_SCOPE_DENIED" {
			t.Fatal("OAR LoadStock must omit WRITE_SCOPE_DENIED")
		}
	}

	extpacks.ClearActive()
	unit := extpacks.GuidanceUnitID("coordinator-gate-blocked")
	desired := extpacks.EmptyDesired()
	desired.Disabled = []string{unit}
	eff2, err := extpackstest.Resolve(t.Context(), desired, nil)
	testutil.FailErr(t, "extpacks.effective catalog resolve failed", err)
	extpacks.SetActive(eff2)
	if eff2.HasLoaded(unit) {
		t.Fatal("disabled guidance must not keep")
	}
	if len(eff2.InspectContributions(unit)) == 0 {
		t.Fatal("inspect must keep contribution body for disabled unit")
	}
}

func TestRefreshEffectiveForProject(t *testing.T) {
	defer extpacks.ClearActive()

	projectDir := t.TempDir()
	lycaonDir := filepath.Join(projectDir, settingsoverlay.DirName())
	if err := os.MkdirAll(lycaonDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	body := []byte("format: 1\ndisabled:\n  - guidance/coordinator-gate-blocked\n")
	if err := os.WriteFile(filepath.Join(lycaonDir, "extensions.yaml"), body, 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	eff, err := extpacks.ResolveCatalog(t.Context(), []string{projectDir}, nil)
	testutil.FailErr(t, "extpacks.ResolveCatalog failed", err)
	extpacks.SetActive(eff)
	if eff.HasLoaded(extpacks.GuidanceUnitID("coordinator-gate-blocked")) {
		t.Fatal("project desired must disable the named guidance unit")
	}
	testutil.FailErr(t, "extpacks.BootError failed", eff.BootError())
}
