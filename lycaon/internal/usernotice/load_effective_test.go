package usernotice_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/usernotice"
)

func TestLoadEffectiveUserNoticesStockParity(t *testing.T) {
	root := configlayout.FindModuleRoot()
	dir := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "host", "user-notices")
	want, err := usernotice.LoadNoticeDir(dir)
	testutil.FailErr(t, "LoadNoticeDir", err)

	content, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "DiscoverStockContent", err)
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   content,
		Desired: extpacks.EmptyDesired(),
	})
	got, err := usernotice.LoadEffectiveUserNotices(eff)
	testutil.FailErr(t, "LoadEffectiveUserNotices", err)
	if len(got.UserNotices) != len(want.UserNotices) {
		t.Fatalf("got %d notices want %d", len(got.UserNotices), len(want.UserNotices))
	}
	for code := range want.UserNotices {
		if _, ok := got.UserNotices[code]; !ok {
			t.Errorf("missing notice %q", code)
		}
	}
	if got.Defaults.Title != want.Defaults.Title {
		t.Fatalf("defaults title=%q want %q", got.Defaults.Title, want.Defaults.Title)
	}
	if len(want.UserNotices) < 80 {
		t.Fatalf("expected stock notice set, got %d", len(want.UserNotices))
	}
}

func TestLoadEffectiveUserNoticesOwnOne(t *testing.T) {
	dir := t.TempDir()
	writeNoticePack(t, dir, "platform", "painted-wolf/platform", map[string]string{
		"host/user-notices/defaults.yaml": "title: Def\nmessage: Def msg\nsuggested_action: Retry\n",
		"host/user-notices/alpha.yaml":    "surfaces: [http]\nnotification: { tier: non_catastrophic, scope: session }\ntitle: stock alpha\nmessage: stock alpha msg\n",
		"host/user-notices/beta.yaml":     "surfaces: [http]\nnotification: { tier: non_catastrophic, scope: session }\ntitle: stock beta\nmessage: stock beta msg\n",
	})
	writeNoticePack(t, dir, "acme", "acme/notices", map[string]string{
		"host/user-notices/alpha.yaml": "surfaces: [http]\nnotification: { tier: non_catastrophic, scope: session }\ntitle: selected alpha\nmessage: selected alpha msg\n",
	})
	platform := inventoryNoticePack(t, dir, "platform", "painted-wolf/platform")
	acme := inventoryNoticePack(t, dir, "acme", "acme/notices")
	desired := extpacks.EmptyDesired()
	desired.Packs = []extpacks.DesiredPack{{ID: "acme/notices"}}
	desired.Own = map[string]string{"host/user-notices/alpha": "acme/notices"}
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   []extpacks.PackContent{platform, acme},
		Desired: desired,
	})
	cfg, err := usernotice.LoadEffectiveUserNotices(eff)
	testutil.FailErr(t, "LoadEffectiveUserNotices", err)
	if cfg.UserNotices["alpha"].Title != "selected alpha" {
		t.Fatalf("alpha title=%q", cfg.UserNotices["alpha"].Title)
	}
	if cfg.UserNotices["beta"].Title != "stock beta" {
		t.Fatalf("beta title changed: %q", cfg.UserNotices["beta"].Title)
	}
}

func TestLoadEffectiveUserNoticesDisableOne(t *testing.T) {
	dir := t.TempDir()
	writeNoticePack(t, dir, "platform", "painted-wolf/platform", map[string]string{
		"host/user-notices/defaults.yaml": "title: Def\nmessage: Def msg\n",
		"host/user-notices/alpha.yaml":    "surfaces: [http]\nnotification: { tier: non_catastrophic, scope: session }\ntitle: alpha\nmessage: alpha msg\n",
		"host/user-notices/beta.yaml":     "surfaces: [http]\nnotification: { tier: non_catastrophic, scope: session }\ntitle: beta\nmessage: beta msg\n",
	})
	platform := inventoryNoticePack(t, dir, "platform", "painted-wolf/platform")
	desired := extpacks.EmptyDesired()
	desired.Disabled = []string{"host/user-notices/alpha"}
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   []extpacks.PackContent{platform},
		Desired: desired,
	})
	if eff.HasLoaded("host/user-notices/alpha") {
		t.Fatal("disabled unit must not load")
	}
	cfg, err := usernotice.LoadEffectiveUserNotices(eff)
	testutil.FailErr(t, "LoadEffectiveUserNotices", err)
	if _, ok := cfg.UserNotices["alpha"]; ok {
		t.Fatal("alpha must be absent")
	}
	if _, ok := cfg.UserNotices["beta"]; !ok {
		t.Fatal("beta must remain")
	}
}

func TestLoadEffectiveUserNoticesRequiresDefaults(t *testing.T) {
	dir := t.TempDir()
	writeNoticePack(t, dir, "platform", "painted-wolf/platform", map[string]string{
		"host/user-notices/alpha.yaml": "surfaces: [http]\nnotification: { tier: non_catastrophic, scope: session }\ntitle: alpha\nmessage: alpha msg\n",
	})
	platform := inventoryNoticePack(t, dir, "platform", "painted-wolf/platform")
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   []extpacks.PackContent{platform},
		Desired: extpacks.EmptyDesired(),
	})
	_, err := usernotice.LoadEffectiveUserNotices(eff)
	if err == nil || !strings.Contains(err.Error(), "defaults") {
		t.Fatalf("missing defaults err=%v", err)
	}
}

func TestLoadEffectiveUserNoticesRequiresCatalog(t *testing.T) {
	_, err := usernotice.LoadEffectiveUserNotices(nil)
	if err == nil || !strings.Contains(err.Error(), "effective catalog required") {
		t.Fatalf("nil eff err=%v", err)
	}
}

func writeNoticePack(t *testing.T, root, leaf, id string, files map[string]string) {
	t.Helper()
	packRoot := filepath.Join(root, leaf)
	if err := os.MkdirAll(packRoot, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	man := "manifest_version: 1\nid: " + id + "\nname: " + id +
		"\nversion: 1.0.0\ncompatibility:\n  extension_api: \"^1.0.0\"\n"
	if err := os.WriteFile(filepath.Join(packRoot, "extension.yaml"), []byte(man), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	for rel, body := range files {
		path := filepath.Join(packRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			testutil.FailErr(t, "create directory", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			testutil.FailErr(t, "write file", err)
		}
	}
}

func inventoryNoticePack(t *testing.T, root, leaf, id string) extpacks.PackContent {
	t.Helper()
	packRoot := filepath.Join(root, leaf)
	man, err := extpacks.LoadManifest(packRoot)
	testutil.FailErr(t, "LoadManifest", err)
	pc, err := extpacks.InventoryPack(extpacks.Pack{
		ID:   id,
		Root: extpacks.OnDisk(packRoot),
	}, man)
	testutil.FailErr(t, "InventoryPack", err)
	return pc
}
