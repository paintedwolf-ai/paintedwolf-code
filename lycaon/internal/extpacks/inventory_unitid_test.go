package extpacks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestUnitIDFromRelWorkflowTemplates(t *testing.T) {
	const pack = "acme/kit"
	cases := []struct {
		rel  string
		want string
	}{
		{"workflows/plan/workflow.yaml", "workflows/plan"},
		{"workflows/_templates/hotfix.yaml", "workflows/_templates/hotfix"},
		{"workflows/_topologies/default-pipeline.yaml", "workflows/_topologies/default-pipeline"},
		{"workflows/_templates/nested/x.yaml", "workflows/_templates/x"},
		{"policy/FOO.yaml", "policy/FOO"},
		{"policy/FOO.yml", "policy/FOO"},
		{"policy/FOO.json", "policy/FOO"},
		{"policy/FOO.txt", ""},
		{"mcp_bindings/fixture_widget.yaml", "mcp_bindings/fixture_widget"},
		{"mcp_bindings/nested/x.yaml", ""},
		{"skills/foo/SKILL.md", "skills/foo"},
		{"skills/foo/references/REFERENCE.md", ""},
		{"skills/foo/scripts/x.py", ""},
		{"skills/foo/sub/SKILL.md", ""},
		{"skills/SKILL.md", ""},
		{"contributions/commands/explain-selection.yaml", "contributions/commands/acme/kit:explain-selection"},
		{"contributions/menus/editor-context.yaml", "contributions/menus/acme/kit:editor-context"},
		{"contributions/keybindings/explain-binding.yaml", "contributions/keybindings/acme/kit:explain-binding"},
		{"contributions/editor-actions/explain.yaml", "contributions/editor-actions/acme/kit:explain"},
		{"contributions/configuration/review-depth.yaml", "contributions/configuration/acme/kit:review-depth"},
		{"contributions/mcp-requirements/github.yaml", "contributions/mcp-requirements/acme/kit:github"},
		{"contributions/search-sources/issues.yaml", "contributions/search-sources/acme/kit:issues"},
		{"contributions/operations/create.yaml", "contributions/operations/acme/kit:create"},
		{"contributions/commands/nested/x.yaml", ""},
		{"contributions/commands/x.yml", ""},
		{"contributions/commands/x.md", ""},
		{"contributions/unknown-kind/x.yaml", ""},
		{"contributions/x.yaml", ""},
	}
	for _, tc := range cases {
		got := UnitIDFor(pack, tc.rel)
		if got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.rel, got, tc.want)
		}
	}
}

// A provider-scoped path has no id without a provider.
func TestUnitIDForRefusesProviderScopedPathWithoutPack(t *testing.T) {
	for _, rel := range []string{
		"contributions/commands/explain-selection.yaml",
		"host/detection-packs/aws-cli/pack.yaml",
	} {
		if got := UnitIDFor("", rel); got != "" {
			t.Fatalf("UnitIDFor(\"\", %q) = %q, want empty", rel, got)
		}
	}
	// Path-addressed kinds still resolve without one.
	if got := UnitIDFor("", "guidance/house-style.md"); got != "guidance/house-style" {
		t.Fatalf("guidance id = %q", got)
	}
}

func TestInventoryPackRejectsUnconsumedYAML(t *testing.T) {
	for _, rel := range []string{"guidance/gate-feedback-wip/stale.yaml", "host/stale.yaml", "agents/_stale.yaml"} {
		t.Run(rel, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, filepath.FromSlash(rel))
			testutil.FailErr(t, "make parent directory", os.MkdirAll(filepath.Dir(path), 0o750))
			testutil.FailErr(t, "write orphan YAML", os.WriteFile(path, []byte("id: stale\n"), 0o600))

			_, err := InventoryPack(Pack{ID: "example/test", Root: OnDisk(dir)}, Manifest{})
			if err == nil || !strings.Contains(err.Error(), "YAML has no catalog unit or fixed-path consumer") {
				t.Fatalf("InventoryPack error = %v, want unconsumed YAML rejection", err)
			}
		})
	}
}

func TestInventoryPackAllowsProfileYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles", "quiet.yaml")
	testutil.FailErr(t, "make profile directory", os.MkdirAll(filepath.Dir(path), 0o750))
	testutil.FailErr(t, "write profile", os.WriteFile(path, []byte("name: quiet\n"), 0o600))

	_, err := InventoryPack(Pack{ID: "example/test", Root: OnDisk(dir)}, Manifest{})
	testutil.FailErr(t, "inventory profile", err)
}

func TestInventoryPackRejectsPlatformYAMLFromAnotherPack(t *testing.T) {
	for _, rel := range []string{"guidance/evidence-kinds.yaml", "workflows/registry.yaml"} {
		t.Run(rel, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, filepath.FromSlash(rel))
			testutil.FailErr(t, "make parent directory", os.MkdirAll(filepath.Dir(path), 0o750))
			testutil.FailErr(t, "write YAML", os.WriteFile(path, []byte("version: 1\n"), 0o600))

			_, err := InventoryPack(Pack{ID: "example/test", Root: OnDisk(dir)}, Manifest{})
			if err == nil || !strings.Contains(err.Error(), "YAML has no catalog unit or fixed-path consumer") {
				t.Fatalf("InventoryPack error = %v, want unconsumed YAML rejection", err)
			}
		})
	}
}

func TestInventoryPackRejectsUnknownPlatformFixedPathYAML(t *testing.T) {
	for _, rel := range []string{
		"host/anchors/stale.yaml",
		"host/secret-mint/stale.yaml",
		"host/detection-pack-upstream/stale.yaml",
	} {
		t.Run(rel, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, filepath.FromSlash(rel))
			testutil.FailErr(t, "make parent directory", os.MkdirAll(filepath.Dir(path), 0o750))
			testutil.FailErr(t, "write YAML", os.WriteFile(path, []byte("version: 1\n"), 0o600))

			_, err := InventoryPack(Pack{ID: "painted-wolf/platform", Root: OnDisk(dir)}, Manifest{})
			if err == nil || !strings.Contains(err.Error(), "YAML has no catalog unit or fixed-path consumer") {
				t.Fatalf("InventoryPack error = %v, want unconsumed YAML rejection", err)
			}
		})
	}
}
