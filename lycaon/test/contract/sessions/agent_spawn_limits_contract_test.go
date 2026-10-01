package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/spawn"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestAgentProfilesForbidSpawnMaxToolLoops(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "agents")
	entries, err := os.ReadDir(dir)
	contractcheck.FailErr(t, "ReadDir agents", err)
	for _, ent := range entries {
		if ent.IsDir() || filepath.Ext(ent.Name()) != ".yaml" {
			continue
		}
		path := filepath.Join(dir, ent.Name())
		raw, err := os.ReadFile(path)
		contractcheck.FailErr(t, "read "+ent.Name(), err)
		if strings.Contains(string(raw), "max_tool_loops:") {
			t.Fatalf("%s must not declare spawn.max_tool_loops; task scope selects the host default", ent.Name())
		}
		var doc struct {
			Spawn map[string]any `yaml:"spawn"`
		}
		contractcheck.FailErr(t, "parse "+ent.Name(), yaml.Unmarshal(raw, &doc))
		if doc.Spawn != nil {
			if _, ok := doc.Spawn["max_tool_loops"]; ok {
				t.Fatalf("%s spawn.max_tool_loops is forbidden; task scope selects the host default", ent.Name())
			}
		}
	}
}

func TestOpenAPITaskMaxToolLoopsMatchesHostCap(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "docs", "openapi", "components", "schemas", "worker.yaml"))
	contractcheck.FailErr(t, "read worker.yaml", err)
	var doc struct {
		Components struct {
			Schemas struct {
				WorkerTask struct {
					Properties struct {
						MaxToolLoops struct {
							Minimum int `yaml:"minimum"`
							Maximum int `yaml:"maximum"`
						} `yaml:"max_tool_loops"`
					} `yaml:"properties"`
				} `yaml:"WorkerTask"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	contractcheck.FailErr(t, "parse worker.yaml", yaml.Unmarshal(raw, &doc))
	budget := spawn.DefaultWorkerToolBudget()
	if max := doc.Components.Schemas.WorkerTask.Properties.MaxToolLoops.Maximum; max != budget.Max {
		t.Fatalf("OpenAPI max_tool_loops.maximum = %d want budget ceiling %d", max, budget.Max)
	}
	if min := doc.Components.Schemas.WorkerTask.Properties.MaxToolLoops.Minimum; min != spawn.WorkerToolBudgetFloor {
		t.Fatalf("OpenAPI max_tool_loops.minimum = %d want host floor %d", min, spawn.WorkerToolBudgetFloor)
	}
}
