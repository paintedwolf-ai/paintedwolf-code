package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// TestShippedPolicyUnitsAreReachable rejects unreachable active policy copy.
func TestShippedPolicyUnitsAreReachable(t *testing.T) {
	t.Parallel()
	dirs, err := filepath.Glob(filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "packs", "painted-wolf", "*", "policy"))
	testutil.FailErr(t, "glob policy dirs", err)
	if len(dirs) == 0 {
		t.Fatal("no policy directories found in the shipped pack")
	}

	seen := 0
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		testutil.FailErr(t, "read "+dir, err)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			rel := filepath.Join(filepath.Base(filepath.Dir(dir)), "policy", e.Name())
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			testutil.FailErr(t, "read "+rel, err)
			var unit struct {
				ID          string `yaml:"id"`
				When        string `yaml:"when"`
				Enforcement string `yaml:"enforcement"`
				Status      string `yaml:"status"`
				Successor   string `yaml:"x-paintedwolf-successor"`
				Related     []struct {
					ID   string `yaml:"id"`
					Type string `yaml:"type"`
				} `yaml:"related"`
			}
			testutil.FailErr(t, "parse "+rel, yaml.Unmarshal(raw, &unit))
			seen++
			if strings.TrimSpace(unit.Enforcement) == "off" {
				t.Errorf("%s: enforcement 'off' ships copy that can never render — delete the unit or give it a real condition", rel)
			}
			retired := deprecatedWithSuccessor(unit.Status, unit.Related) ||
				(strings.TrimSpace(unit.Status) == "deprecated" && strings.TrimSpace(unit.Successor) != "")
			if when := strings.TrimSpace(unit.When); when == "false" && !retired {
				t.Errorf("%s: when 'false' is unreachable by construction — the agent would receive the bare code %q", rel, unit.ID)
			}
		}
	}
	if seen == 0 {
		t.Fatalf("no policy units found under %v", dirs)
	}
}

func deprecatedWithSuccessor(status string, related []struct {
	ID   string `yaml:"id"`
	Type string `yaml:"type"`
}) bool {
	if strings.TrimSpace(status) != "deprecated" {
		return false
	}
	for _, relation := range related {
		if strings.TrimSpace(relation.ID) != "" && strings.TrimSpace(relation.Type) == "obsolete" {
			return true
		}
	}
	return false
}
