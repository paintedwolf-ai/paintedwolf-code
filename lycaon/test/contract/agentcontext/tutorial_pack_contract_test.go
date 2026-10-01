package contract

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil/extstatetest"
	"github.com/lycaon/lycaon/internal/workflowvalidate"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Tutorial fixtures mirror the published pack walkthrough.

func tutorialPackRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "fixtures", "tutorial-pack")
}

func TestTutorialPackInventories(t *testing.T) {
	t.Parallel()
	root := tutorialPackRoot(t)
	man, err := extpacks.LoadManifest(root)
	contractcheck.FailErr(t, "load manifest", err)
	if man.ID != "acme/triage-lite" {
		t.Fatalf("pack id = %q want acme/triage-lite", man.ID)
	}
	if !extpacks.ManifestHostCompatible(man) {
		t.Fatalf("extension API %q is incompatible with host %s",
			man.Compatibility.ExtensionAPI, extpacks.ExtensionAPIVersion)
	}
	pc, err := extpacks.InventoryPack(extpacks.Pack{ID: man.ID, Root: extpacks.OnDisk(root)}, man)
	contractcheck.FailErr(t, "inventory pack", err)

	want := map[string]bool{
		"guidance/triage-lite-plan":          false,
		"policy/TRIAGE_LITE_WRITE_FORBIDDEN": false,
		"workflows/triage-lite":              false,
	}
	for _, u := range pc.Units {
		if _, ok := want[u.ID]; !ok {
			t.Fatalf("unexpected unit %q — the tutorial's file list is out of date", u.ID)
		}
		want[u.ID] = true
	}
	for id, seen := range want {
		if !seen {
			t.Fatalf("tutorial pack no longer provides unit %q", id)
		}
	}
}

func TestTutorialPackWorkflowValidates(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	manifest := filepath.Join(tutorialPackRoot(t), "workflows", "triage-lite", "workflow.yaml")
	diags, err := workflowvalidate.ValidateCatalog(t.Context(), workflowvalidate.CatalogValidateOptions{
		ConfigRoot: filepath.Join(root, "lycaon"),
		Mode:       workflowvalidate.ModePaths,
		Paths:      []string{manifest},
	})
	contractcheck.FailErr(t, "ValidateCatalog", err)
	if len(diags) > 0 {
		for _, d := range diags {
			t.Logf("%s [%s] %s", d.Field, d.Code, d.Message)
		}
		t.Fatalf("tutorial workflow produced %d diagnostics", len(diags))
	}
}

func TestTutorialPackRuleLoadsAndConforms(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	packRoot := tutorialPackRoot(t)
	contractcheck.FailErr(t, "install catalog", anchorcatalog.InstallFile(
		filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml")))

	schemaDir := filepath.Join(root, "schemas")
	loader, err := oar.NewLoader(schemaDir)
	contractcheck.FailErr(t, "oar loader", err)
	rs, err := loader.LoadDir(extpacks.OnDisk(filepath.Join(packRoot, "policy")))
	contractcheck.FailErr(t, "load policy dir", err)
	rule, ok := rs.Get("TRIAGE_LITE_WRITE_FORBIDDEN")
	if !ok {
		t.Fatal("tutorial rule did not load")
	}
	if rule.Effect != "block" {
		t.Fatalf("rule effect = %q want block", rule.Effect)
	}

	// Both directions: the rule fires inside the workflow's posture and stays
	// quiet outside it. This pair is what the tutorial tells the reader to run.
	cr, err := oar.NewConformanceRunner(schemaDir)
	contractcheck.FailErr(t, "conformance runner", err)
	fixtures, err := oar.LoadFixturesDir(filepath.Join(packRoot, "conformance"))
	contractcheck.FailErr(t, "load fixtures", err)
	if len(fixtures) != 2 {
		t.Fatalf("tutorial conformance fixtures = %d want 2 (one firing, one not)", len(fixtures))
	}
	for _, fx := range fixtures {
		if err := cr.RunFixture(fx); err != nil {
			t.Errorf("%s: %v", fx.Name, err)
		}
	}
}

func TestTutorialPackRuleCopyRenders(t *testing.T) {
	packRoot := tutorialPackRoot(t)
	cfg, err := guidance.LoadValidatedHintConfig(extpacks.OnDisk(filepath.Join(packRoot, "policy")))
	contractcheck.FailErr(t, "load rule copy", err)
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(engine))
	formatter := guidance.NewStaticRejectFormatter(cfg)

	entry, ok := cfg.HintCodes["TRIAGE_LITE_WRITE_FORBIDDEN"]
	if !ok {
		t.Fatal("tutorial rule is missing from the rendered copy registry")
	}
	if len(entry.Scenarios) == 0 {
		t.Fatal("tutorial rule ships no scenarios — the tutorial teaches writing them")
	}
	for _, sc := range entry.Scenarios {
		out, err := formatter.Format("TRIAGE_LITE_WRITE_FORBIDDEN", guidance.ScenarioVars(entry, sc))
		contractcheck.FailErr(t, "format reject", err)
		for _, want := range sc.ExpectContains {
			if !strings.Contains(out, want) {
				t.Fatalf("scenario %q: rendered reject is missing %q in:\n%s", sc.ID, want, out)
			}
		}
	}
}

func TestTutorialPackGuidanceTemplateRenders(t *testing.T) {
	// Templates resolve as pack units, so the tutorial's guidance file is
	// reachable only once the pack is installed, and the ref carries its
	// guidance/ prefix.
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	extstatetest.InstallPack(t, extstatetest.DeviceScope(), "path:"+tutorialPackRoot(t), "", "")

	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	out, err := engine.Render(context.Background(), "guidance/triage-lite-plan", map[string]any{})
	contractcheck.FailErr(t, "render guidance template", err)
	if strings.TrimSpace(out) == "" {
		t.Fatal("tutorial inject template rendered empty")
	}
	for _, leak := range []string{"{%", "{{"} {
		if strings.Contains(out, leak) {
			t.Fatalf("tutorial inject template leaks unrendered %q:\n%s", leak, out)
		}
	}
}
