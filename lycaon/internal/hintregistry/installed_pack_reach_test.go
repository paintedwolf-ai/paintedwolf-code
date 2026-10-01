package hintregistry_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extstatetest"
)

func TestInstalledPackPolicyReachesRegistry(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfgDir)

	author := filepath.Join(t.TempDir(), "gating-policy")
	const code = "ACME_POST_INVOKE_NOTE"
	writePolicyPack(t, author, "acme/gating", code, "tool.post_invoke")

	extstatetest.InstallPack(t, extstatetest.DeviceScope(), "path:"+author, "", "")

	cat, err := extpacks.ResolveCatalog(t.Context(), nil, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	unitID := extpacks.PolicyUnitID(code)
	if !cat.HasLoaded(unitID) {
		t.Fatalf("resolve did not load %s", unitID)
	}

	entries, err := hintregistry.ListEffectiveWithCatalog(cat)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, e := range entries {
		if e.Code == code {
			return
		}
	}
	t.Fatalf("installed pack's policy unit %s is loaded in the catalog but absent from the registry (%d entries)",
		unitID, len(entries))
}

func TestDoc9InstallingDuplicatePolicyIdentityIsRejected(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	stock, err := extpacks.ResolveCatalog(t.Context(), nil, nil)
	testutil.FailErr(t, "resolve stock catalog", err)
	entries, err := hintregistry.ListEffectiveWithCatalog(stock)
	testutil.FailErr(t, "list stock policy entries", err)
	code := entries[0].Code
	author := filepath.Join(t.TempDir(), "duplicate-pack")
	writePolicyPack(t, author, "acme/duplicate", code, "tool.pre_invoke")
	owner := extstatetest.Owner(t)
	if _, err := extstatetest.TryApply(t, owner, extstatetest.DeviceScope(), extensionstate.InstallOp{Source: "path:" + author}); err == nil || !strings.Contains(err.Error(), "OAR-DOC-9") {
		t.Fatalf("[OAR-DOC-9] duplicate install error = %v", err)
	}
	current, err := extpacks.ResolveCatalog(t.Context(), nil, nil)
	testutil.FailErr(t, "resolve catalog after rejected install", err)
	if !current.HasLoaded(extpacks.PolicyUnitID(code)) {
		t.Fatal("[OAR-DOC-9] rejected install displaced stock policy")
	}
}

func writePolicyPack(t *testing.T, dir, packID, code, anchor string) {
	t.Helper()
	mustWriteFile(t, filepath.Join(dir, "extension.yaml"),
		"manifest_version: 1\nid: "+packID+"\nname: test\nversion: 1.0.0\n"+
			"compatibility:\n  extension_api: \"^1.0.0\"\ndependencies:\n  painted-wolf/platform:\n    version: \"^1.0.0\"\n")
	mustWriteFile(t, filepath.Join(dir, "policy", code+".yaml"),
		"oar: '1.0'\nid: "+code+"\nkind: policy\nanchor: "+anchor+"\n"+
			"requires:\n  profiles:\n    - tool\nwhen: 'false'\neffect: warn\n"+
			"copy:\n  cause: c\n  why: w\n  fix: f\n  instead: i\n"+
			"x-paintedwolf-emit: rule:test\nx-paintedwolf-message: m\n")
}

func mustWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
}

func TestDoc1InstalledJSONPolicyReachesRegistry(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	author := filepath.Join(t.TempDir(), "json-policy")
	writePolicyPack(t, author, "acme/json", "BASE_RULE", "tool.post_invoke")
	const document = `{"oar":"1.0","namespace":"acme.json","id":"JSON_RULE","kind":"policy","anchor":"model.output","requires":{"profiles":["content"]},"when":"content_length == 12","effect":"transform","transform":{"action":"replace","target":"content","replacement":"Reviewed \ud83d\udc3a"}}`
	mustWriteFile(t, filepath.Join(author, "policy", "portable.json"), document)
	extstatetest.InstallPack(t, extstatetest.DeviceScope(), "path:"+author, "", "")
	cat, err := extpacks.ResolveCatalog(t.Context(), nil, nil)
	testutil.FailErr(t, "resolve JSON policy", err)
	entries, err := hintregistry.ListEffectiveWithCatalog(cat)
	testutil.FailErr(t, "load installed JSON policy", err)
	for _, entry := range entries {
		if entry.Code == "acme.json/JSON_RULE" {
			if !strings.Contains(string(entry.Body), "Reviewed 🐺") {
				t.Fatalf("[OAR-DOC-1] JSON Unicode lost: %s", entry.Body)
			}
			return
		}
	}
	t.Fatal("[OAR-DOC-1] installed JSON rule missing from registry")
}
