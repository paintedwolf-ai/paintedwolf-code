package orchestration

import (
	"context"
	"github.com/lycaon/lycaon/internal/agentdef"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/extpacks"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestNewRegistryEmptyWithoutDefaults(t *testing.T) {
	reg := NewMemoryAgentRegistry()
	if len(reg.List()) != 0 {
		t.Fatalf("expected empty registry, got %d agents", len(reg.List()))
	}
}

func TestAgentRegistryLoadsBundledYAML(t *testing.T) {
	reg := NewMemoryAgentRegistry()
	if err := LoadRequiredAgentRegistry(context.Background(), reg); err != nil {
		testutil.FailErr(t, "LoadRequiredAgentRegistry failed", err)
	}
	p, err := reg.Get("implementer")
	testutil.FailErr(t, "reg.Get failed", err)
	if p.ToolProfile != "implement" {
		t.Fatalf("tool_profile = %q", p.ToolProfile)
	}
	if _, err := reg.Get("nonexistent-agent"); err == nil {
		t.Fatal("expected error for unknown agent id")
	}
}

// The registry loads from the contributing packs, so a caller cannot reach "no
// agents on disk". The fail-closed half still has to hold: a build whose packs
// contribute no agent profiles must refuse to boot rather than run with an empty
// roster.
func TestLoadRequiredAgentRegistryFailsClosedOnEmptyRoster(t *testing.T) {
	configtest.Only(t, map[config.Rel]string{})
	reg := NewMemoryAgentRegistry()
	if err := LoadRequiredAgentRegistry(context.Background(), reg); err == nil {
		t.Fatal("expected an error when no pack contributes agent profiles")
	}
}

func TestLoadRequiredAgentRegistryLoadsBundledRoster(t *testing.T) {
	reg := NewMemoryAgentRegistry()
	testutil.FailErr(t, "LoadRequiredAgentRegistry", LoadRequiredAgentRegistry(context.Background(), reg))
	if _, err := reg.Get("implementer"); err != nil {
		t.Fatalf("bundled roster missing implementer: %v", err)
	}
}

func TestLoadAgentProfilesRejectsDuplicateIDs(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.yaml", "b.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("id: dupe\nname: x\ntool_profile: coordinator\n"), 0o644); err != nil {
			testutil.FailErr(t, "write file", err)
		}
	}
	if _, err := agentdef.LoadDir(extpacks.OnDisk(dir)); err == nil {
		t.Fatal("expected duplicate id error")
	}
}

func TestComposeEnforcesMaxTeamAgents(t *testing.T) {
	reg := NewMemoryAgentRegistryForTest()
	ids := []string{"coordinator", "implementer", "repo-researcher", "code-reviewer", "path-explorer", "plan-writer"}
	_, err := reg.Compose(TeamStrategyParallel, ids)
	if err == nil {
		t.Fatal("expected max team agents error")
	}
	_, err = reg.Compose(TeamStrategyParallel, ids[:MaxTeamAgents])
	testutil.FailErr(t, "reg.Compose failed", err)
}
