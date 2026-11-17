package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/approvals"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestApprovalExplanationCoverage(t *testing.T) {
	t.Parallel()
	cfg, err := approvals.LoadConfigStock()
	contractcheck.FailErr(t, "LoadConfigStock", err)

	gateable := approvals.GateableKeys()
	gateableSet := make(map[string]bool, len(gateable))
	for _, key := range gateable {
		gateableSet[key] = true
		if _, ok := cfg.Explanations[key]; !ok {
			t.Fatalf("gateable tool %q missing explain.yaml entry", key)
		}
	}
	for key := range cfg.Explanations {
		if !gateableSet[key] {
			t.Fatalf("orphan approval explanation entry %q — not in GateableKeys()", key)
		}
	}
}

func TestApprovalExplanationScenariosRender(t *testing.T) {
	t.Parallel()
	cfg, err := approvals.LoadConfigStock()
	contractcheck.FailErr(t, "LoadConfigStock", err)

	for key, entry := range cfg.Explanations {
		var defaultSc *approvals.ScenarioEntry
		for i := range entry.Scenarios {
			if entry.Scenarios[i].ID == "default" {
				defaultSc = &entry.Scenarios[i]
				break
			}
		}
		if defaultSc == nil {
			t.Fatalf("%q missing default scenario", key)
		}
		vars := approvals.ScenarioVars(*defaultSc)
		copy := approvals.RenderEntry(entry, vars)
		if strings.TrimSpace(copy.What) == "" {
			t.Fatalf("%q rendered empty what_changes", key)
		}
		if strings.TrimSpace(copy.IfWrong) == "" {
			t.Fatalf("%q rendered empty if_wrong", key)
		}
	}
}
