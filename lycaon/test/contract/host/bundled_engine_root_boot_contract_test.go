package contract

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/orchestration"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// A release stages no config tree — packs resolve from the engine binary's
// embedded FS. Every other registry test points at the repo checkout, so a boot
// path that reads packs off disk passes CI and still fails in the shipped .app.
// This boots the registry with no config on disk and no override set.
func TestReleaseBootLoadsStockPackAgentsFromEmbed(t *testing.T) {
	t.Parallel()
	engineRoot := stageBundledEngineRoot(t)
	if _, err := os.Stat(filepath.Join(engineRoot, "config")); err == nil {
		t.Fatal("staged engine-root must not contain config/ — the engine embeds it")
	}

	reg := orchestration.NewMemoryAgentRegistry()
	if err := orchestration.LoadRequiredAgentRegistry(context.Background(), reg); err != nil {
		contractcheck.FailErr(t, "load agent registry from embedded config", err)
	}

	// Gate agents may come from sibling embedded packs.
	if err := orchestration.ValidateGateAgents(reg); err != nil {
		contractcheck.FailErr(t, "validate gate agents resolve from embedded config", err)
	}
}

// schemas/ is not embedded, so a release resolves it from the staged engine
// root. The resolver and the staging script must agree on where it lands.
func TestStagedEngineRootResolvesSchemasDir(t *testing.T) {
	engineRoot := stageBundledEngineRoot(t)
	t.Setenv(configlayout.EnvEngineRoot, engineRoot)
	if got := configlayout.SchemasDir(""); got == "" {
		t.Fatalf("no schemas dir resolved for staged engine-root %s", engineRoot)
	}
}

// stageBundledEngineRoot lays out a temp dir like the release bundle's
// Resources/engine-root — without go.mod and without config/.
func stageBundledEngineRoot(t *testing.T) string {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	engineRoot := t.TempDir()
	src := filepath.Join(root, configlayout.SchemasDirName)
	dst := filepath.Join(engineRoot, configlayout.SchemasDirName)
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		contractcheck.FailErr(t, "stage "+src+" into bundle engine-root layout", err)
	}
	if _, err := os.Stat(filepath.Join(engineRoot, "go.mod")); err == nil {
		t.Fatal("staged engine-root must not contain go.mod — the shipped bundle has none")
	}
	return engineRoot
}
