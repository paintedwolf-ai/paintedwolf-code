package extpacks_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/testutil/extstatetest"
)

func TestDiscoverCachedRefusesStockIDShadow(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfgDir)

	author := filepath.Join(t.TempDir(), "acme-pack")
	extpackstest.WriteMinimalPack(t, author, "acme/shadow", 1)
	installLinked(t, author)

	rewritePackID(t, author, "painted-wolf/security")

	found, err := extpacks.DiscoverLockedContent(nil)
	testutil.FailErr(t, "DiscoverLockedContent", err)
	shadow := findPack(t, found, "acme/shadow")
	if shadow.OmitReason != extpacks.BlockedInvalid {
		t.Fatalf("shadow omit = %q want invalid", shadow.OmitReason)
	}
	if len(shadow.Units) != 0 {
		t.Fatalf("identity-mismatched path pack must not contribute units: %d", len(shadow.Units))
	}

	stock, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "DiscoverStockContent", err)
	security := findPack(t, stock, "painted-wolf/security")
	if len(security.Units) == 0 {
		t.Fatal("shipped painted-wolf/security was replaced by the cached pack")
	}
	if security.Kind != extpacks.PackKindStock {
		t.Fatalf("painted-wolf/security kind=%s want stock", security.Kind)
	}

}

func TestDiscoverCachedRefusesInstalledIDMismatch(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfgDir)

	author := filepath.Join(t.TempDir(), "acme-pack")
	extpackstest.WriteMinimalPack(t, author, "acme/shadow", 1)
	installLinked(t, author)
	rewritePackID(t, author, "acme/other")

	found, err := extpacks.DiscoverLockedContent(nil)
	testutil.FailErr(t, "DiscoverLockedContent", err)
	shadow := findPack(t, found, "acme/shadow")
	if shadow.OmitReason != extpacks.BlockedInvalid {
		t.Fatalf("id-mismatch omit = %q want invalid", shadow.OmitReason)
	}
	if len(shadow.Units) != 0 {
		t.Fatalf("identity-mismatched path pack must not contribute units: %d", len(shadow.Units))
	}
}

func TestDiscoverCachedKeepsMatchingID(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfgDir)

	author := filepath.Join(t.TempDir(), "acme-pack")
	extpackstest.WriteMinimalPack(t, author, "acme/shadow", 1)
	installLinked(t, author)

	cached, err := extpacks.DiscoverLockedContent(nil)
	testutil.FailErr(t, "DiscoverLockedContent", err)
	if len(cached) != 1 || cached[0].Pack.ID != "acme/shadow" {
		t.Fatalf("cached=%+v", cached)
	}
	if len(cached[0].Units) == 0 {
		t.Fatal("well-formed pack must still contribute units")
	}
	if len(cached[0].Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %v", cached[0].Diagnostics)
	}
}

func TestInventoryPackRefusesSymlinkedUnitFile(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "id_rsa")
	if err := os.WriteFile(secret, []byte("-----BEGIN PRIVATE KEY-----\n"), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	packRoot := filepath.Join(t.TempDir(), "leaky")
	extpackstest.WriteMinimalPack(t, packRoot, "acme/leaky", 1)
	link := filepath.Join(packRoot, "guidance", "leak.md")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	pc, err := extpacks.InventoryPack(
		extpacks.Pack{ID: "acme/leaky", Root: extpacks.OnDisk(packRoot)},
		extpacks.Manifest{ID: "acme/leaky"},
	)
	testutil.FailErr(t, "InventoryPack", err)
	for _, u := range pc.Units {
		if u.ID == "guidance/leak" {
			t.Fatalf("symlinked unit published %d bytes of linked content", len(u.Content))
		}
		if strings.Contains(string(u.Content), "PRIVATE KEY") {
			t.Fatalf("unit %s leaked linked file content", u.ID)
		}
	}
	found := false
	for _, d := range pc.Diagnostics {
		if d.Code == extpacks.DiagUnitNotRegular && d.UnitID == "guidance/leak" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no %s diagnostic: %v", extpacks.DiagUnitNotRegular, pc.Diagnostics)
	}
}

func TestInstallRefusesSymlinkIntoExtensionsCache(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfgDir)

	cacheRoot, err := extpacks.CacheRoot()
	testutil.FailErr(t, "CacheRoot", err)
	inside := filepath.Join(cacheRoot, "smuggled")
	extpackstest.WriteMinimalPack(t, inside, "acme/smuggled", 1)

	link := filepath.Join(t.TempDir(), "outside")
	if err := os.Symlink(inside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err = extstatetest.TryApply(t, extstatetest.Owner(t), extstatetest.DeviceScope(),
		extensionstate.InstallOp{Source: "path:" + link})
	if err == nil || !strings.Contains(err.Error(), "inside the extensions cache") {
		t.Fatalf("err=%v; symlink into the cache must be refused", err)
	}
}

func installLinked(t *testing.T, author string) {
	t.Helper()
	extstatetest.InstallPack(t, extstatetest.DeviceScope(), "path:"+author, "", "")
}

func rewritePackID(t *testing.T, author, id string) {
	t.Helper()
	path := filepath.Join(author, "extension.yaml")
	data, err := os.ReadFile(path)
	testutil.FailErr(t, "read extension.yaml", err)
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "id:") {
			lines[i] = "id: " + id
		}
	}
	testutil.FailErr(t, "write extension.yaml", os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644))
}

func findPack(t *testing.T, all []extpacks.PackContent, id string) extpacks.PackContent {
	t.Helper()
	for _, pc := range all {
		if pc.Pack.ID == id {
			return pc
		}
	}
	t.Fatalf("pack %q not discovered", id)
	return extpacks.PackContent{}
}
