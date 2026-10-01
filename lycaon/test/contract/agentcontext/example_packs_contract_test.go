package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil/extstatetest"
	"github.com/lycaon/lycaon/internal/workflowvalidate"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Example pack fixtures cover each authorable extension surface.

func examplePacksRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "fixtures", "example-packs")
}

// exampleUnits lists the units each fixture provides.
var exampleUnits = map[string][]string{
	"credential-recognition": {
		"host/credential-slots/example/credential-recognition:service",
	},
	"house-rules": {
		"approvals/rules/generated-ts",
	},
	"docs-writer": {
		"agents/example-docs-writer",
		"agents/prompts/example-docs-writer",
		"tools/profiles/example_docs_only",
	},
	"tool-voice": {
		"tools/schemas/wc",
	},
	"team-flow": {
		"workflows/example-triage",
		"workflows/_topologies/example-triage-sweep",
	},
	"tracker-facts": {
		"mcp_bindings/example_issue",
	},
	"onboarding-copy": {
		"approvals/git_commit/explain",
		"guidance/example-house-style",
		"host/bindings/example-house-style",
		"host/user-notices/EXAMPLE_ONBOARDING_INCOMPLETE",
		"shared/partials/example-evidence-first",
	},
	// Contribution unit ids carry the providing pack, so they read as the
	// declaration's own identifier under its kind root.
	"editor-surface": {
		"contributions/commands/example/editor-surface:create-issue",
		"contributions/commands/example/editor-surface:read-tracker-issue",
		"contributions/commands/example/editor-surface:summarize-selection",
		"contributions/configuration/example/editor-surface:offer-summarize",
		"contributions/editor-actions/example/editor-surface:summarize",
		"contributions/keybindings/example/editor-surface:key-summarize-selection",
		"contributions/mcp-requirements/example/editor-surface:tracker",
		"contributions/menus/example/editor-surface:editor-context-summarize",
		"contributions/operations/example/editor-surface:perform-create-issue",
		"contributions/search-sources/example/editor-surface:issues",
		"contributions/themes/example/editor-surface:example-slate",
		"guidance/example-summarize-selection",
	},
}

// The example packs carry a worked example of every contribution kind a
// third-party pack can use to reach Den.
func TestExamplePacksCoverEveryContributionKind(t *testing.T) {
	t.Parallel()
	covered := map[string]bool{}
	for _, units := range exampleUnits {
		for _, id := range units {
			if root, _, ok := strings.Cut(strings.TrimPrefix(id, "contributions/"), "/"); ok &&
				strings.HasPrefix(id, "contributions/") {
				covered[root] = true
			}
		}
	}
	for _, kind := range contribution.Kinds() {
		if !covered[string(kind)] {
			t.Errorf("no example pack declares a %s contribution — an author has nothing to copy", kind)
		}
	}
}

func TestExamplePacksInventoryExactly(t *testing.T) {
	t.Parallel()
	root := examplePacksRoot(t)
	for leaf, want := range exampleUnits {
		t.Run(leaf, func(t *testing.T) {
			t.Parallel()
			packRoot := filepath.Join(root, leaf)
			man, err := extpacks.LoadManifest(packRoot)
			contractcheck.FailErr(t, "load manifest", err)
			if !extpacks.ManifestHostCompatible(man) {
				t.Fatalf("extension API %q is incompatible with host %s",
					man.Compatibility.ExtensionAPI, extpacks.ExtensionAPIVersion)
			}
			pc, err := extpacks.InventoryPack(extpacks.Pack{ID: man.ID, Root: extpacks.OnDisk(packRoot)}, man)
			contractcheck.FailErr(t, "inventory pack", err)

			got := map[string]bool{}
			for _, u := range pc.Units {
				got[u.ID] = true
			}
			for _, id := range want {
				if !got[id] {
					t.Errorf("example pack %s no longer provides %q", leaf, id)
				}
				delete(got, id)
			}
			for id := range got {
				t.Errorf("example pack %s provides undeclared unit %q — add it to exampleUnits and to the README table", leaf, id)
			}
		})
	}
}

func TestExamplePacksAllCovered(t *testing.T) {
	t.Parallel()
	root := examplePacksRoot(t)
	ents, err := os.ReadDir(root)
	contractcheck.FailErr(t, "read example-packs", err)
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		if _, ok := exampleUnits[ent.Name()]; !ok {
			t.Errorf("example pack %q has no entry in exampleUnits — add one, or it is untested", ent.Name())
		}
	}
}

// The example workflow passes the custom-overlay validator. Its topology ships in
// the same pack and resolves through the effective catalog, so the pack is
// installed before validation.
func TestExamplePackWorkflowValidates(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	extstatetest.InstallPack(t, extstatetest.DeviceScope(),
		"path:"+filepath.Join(examplePacksRoot(t), "team-flow"), "", "")
	if _, err := extpacks.ApplyCatalog(t.Context(), nil, nil); err != nil {
		t.Fatalf("resolve after install: %v", err)
	}
	t.Cleanup(extpacks.ClearActive)

	manifest := filepath.Join(examplePacksRoot(t), "team-flow", "workflows", "example-triage", "workflow.yaml")
	diags, err := workflowvalidate.ValidateCatalog(t.Context(), workflowvalidate.CatalogValidateOptions{
		ConfigRoot: filepath.Join(contractcheck.RepoRoot(t), "lycaon"),
		Mode:       workflowvalidate.ModePaths,
		Paths:      []string{manifest},
	})
	contractcheck.FailErr(t, "ValidateCatalog", err)
	if len(diags) > 0 {
		for _, d := range diags {
			t.Logf("%s [%s] %s", d.Field, d.Code, d.Message)
		}
		t.Fatalf("example workflow produced %d diagnostics", len(diags))
	}
}
