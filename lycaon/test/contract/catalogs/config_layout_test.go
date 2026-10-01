package contract

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestConfigRootLayout(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cfgDir := filepath.Join(root, "lycaon", "config")
	entries, err := os.ReadDir(cfgDir)
	contractcheck.FailErr(t, "read config dir", err)

	allowed := map[string]bool{}
	for _, name := range configlayout.AllowedConfigRootEntries() {
		allowed[name] = true
	}
	for _, ent := range entries {
		name := ent.Name()
		if name == ".DS_Store" {
			continue
		}
		if !allowed[name] {
			t.Fatalf("unexpected top-level config entry %q — allowed: %v", name, configlayout.AllowedConfigRootEntries())
		}
	}
	for _, must := range []string{"packs", "fixtures"} {
		if _, err := os.Stat(filepath.Join(cfgDir, must)); err != nil {
			t.Fatalf("missing required config dir %q: %v", must, err)
		}
	}
	for _, orphan := range []string{"catalog", "guidance", "prompts", "agents", "workflows", "tool-profiles", "scanners"} {
		if _, err := os.Stat(filepath.Join(cfgDir, orphan)); err == nil {
			t.Fatalf("orphan directory still at config root: %q", orphan)
		}
	}
}

func TestCatalogHintFeaturesHaveManifest(t *testing.T) {
	t.Parallel()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "LoadHintConfigStock", err)
	// Assert host-critical hint families.
	for _, code := range []string{
		"COORDINATOR_BATCH_WRONG_PHASE",
		"COORDINATOR_WORKER_IN_FLIGHT",
		"PROGRESS_MISSING",
		"WRITE_SCOPE_DENIED",
		"READ_PATH_NOT_FOUND",
		"GREP_SCOPE_REQUIRED",
		"WORKFLOW_GATE_BLOCKED",
	} {
		if _, ok := cfg.HintCodes[code]; !ok {
			t.Fatalf("stock hint catalog missing required code %q", code)
		}
	}
}

func TestCatalogWorkflowFeaturesHaveManifest(t *testing.T) {
	t.Parallel()
	catalog, err := extpacks.CatalogForConsumers()
	contractcheck.FailErr(t, "catalog for consumers", err)
	manifests, _, err := workflowdef.LoadManifestsFromCatalog(catalog)
	contractcheck.FailErr(t, "load bundled workflow manifests", err)
	// Assert each stock workflow resolves.
	ids := map[string]bool{}
	for _, m := range manifests {
		ids[m.ID] = true
	}
	for _, id := range []string{
		"bugbash", "implement", "options", "plan", "recon-pack", "security-survey",
	} {
		if !ids[id] {
			t.Fatalf("workflow feature %q missing a resolved manifest (have %d: %v)", id, len(ids), ids)
		}
	}
}
