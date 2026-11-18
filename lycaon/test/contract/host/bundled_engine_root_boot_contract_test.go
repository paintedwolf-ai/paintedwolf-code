package contract

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/orchestration"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// A release stages no config tree: packs resolve from the engine binary's
// embedded FS. This boots the registry against a staged engine root, from a
// working directory outside the checkout, with no config override.
func TestReleaseBootLoadsStockPackAgentsFromEmbed(t *testing.T) {
	if root := os.Getenv(config.EnvConfigRoot); root != "" {
		t.Fatalf("%s=%q redirects bundled reads to disk", config.EnvConfigRoot, root)
	}
	if !config.SourceImmutable() {
		t.Fatal("bundled configuration is not the embedded tree")
	}
	t.Setenv(configlayout.EnvEngineRoot, stageBundledEngineRoot(t))
	t.Chdir(t.TempDir())

	reg := orchestration.NewMemoryAgentRegistry()
	if err := orchestration.LoadRequiredAgentRegistry(context.Background(), reg); err != nil {
		contractcheck.FailErr(t, "load agent registry from embedded config", err)
	}
	if len(reg.List()) == 0 {
		t.Fatal("embedded agent registry is empty")
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
// Resources/engine-root: schemas only.
func stageBundledEngineRoot(t *testing.T) string {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	engineRoot := t.TempDir()
	src := filepath.Join(root, configlayout.SchemasDirName)
	dst := filepath.Join(engineRoot, configlayout.SchemasDirName)
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		contractcheck.FailErr(t, "stage "+src+" into bundle engine-root layout", err)
	}
	return engineRoot
}
