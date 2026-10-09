package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"gopkg.in/yaml.v3"
)

const inventoryCheck = "codegen:dependency-inventory:check"

// Feature changes never regenerate dependency evidence: a scheduled workflow
// refreshes the inventory and proposes it as its own pull request, so no gate
// requires the inventory to match upstream.
func TestInventoryIsMaintainedOnSchedule(t *testing.T) {
	t.Parallel()
	repo := filepath.Join("..", "..", "..")

	body, err := os.ReadFile(filepath.Join(repo, "scripts/verification-plan.json"))
	testutil.FailErr(t, "read verification plan", err)
	var plan struct {
		Groups map[string][]string `json:"groups"`
	}
	testutil.FailErr(t, "decode verification plan", json.Unmarshal(body, &plan))
	for name, members := range plan.Groups {
		if name != inventoryCheck && slices.Contains(members, inventoryCheck) {
			t.Errorf("group %s runs %s; scheduled maintenance owns the inventory", name, inventoryCheck)
		}
	}

	body, err = os.ReadFile(filepath.Join(repo, ".github/workflows/dependency-inventory.yml"))
	testutil.FailErr(t, "read maintenance workflow", err)
	var workflow struct {
		On   map[string]any `yaml:"on"`
		Jobs map[string]struct {
			Steps []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	testutil.FailErr(t, "decode maintenance workflow", yaml.Unmarshal(body, &workflow))
	if _, ok := workflow.On["schedule"]; !ok {
		t.Error("dependency-inventory.yml must run on a schedule")
	}
	var runs []string
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			runs = append(runs, strings.TrimSpace(step.Run))
		}
	}
	for _, want := range []string{"./task codegen:dependency-inventory:upstream", "python3 -m ci_policy.maintenance"} {
		if !slices.Contains(runs, want) {
			t.Errorf("dependency-inventory.yml must run %q; runs %q", want, runs)
		}
	}

	body, err = os.ReadFile(filepath.Join(repo, "scripts/ci_policy/maintenance.py"))
	testutil.FailErr(t, "read maintenance publisher", err)
	for _, output := range []string{snapshotRel, inventoryRel, dependabotRel} {
		if !strings.Contains(string(body), "'"+output+"'") {
			t.Errorf("maintenance publisher omits generated %s", output)
		}
	}
}
