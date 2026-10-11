package contract

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/approvals"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
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
		for _, want := range defaultSc.ExpectContains {
			if !strings.Contains(copy.What, want) && !strings.Contains(copy.Who, want) && !strings.Contains(copy.IfWrong, want) {
				t.Fatalf("%q default scenario missing %q in rendered copy: what=%q who=%q if_wrong=%q",
					key, want, copy.What, copy.Who, copy.IfWrong)
			}
		}
	}
}

func TestApprovalExplanationNoInlineProse(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	scan, err := scanInlineApprovalProse(lycaonRoot)
	contractcheck.FailErr(t, "scanInlineApprovalProse", err)
	if len(scan.InlineApprovalProse) > 0 {
		t.Fatalf("inline approval consequence prose on gate path (use approvals registry):\n%s",
			strings.Join(scan.InlineApprovalProse, "\n"))
	}
}

func TestApprovalExplainActionIntegration(t *testing.T) {
	t.Parallel()
	reg, err := approvals.LoadRegistryStock()
	contractcheck.FailErr(t, "LoadRegistryStock", err)

	got := reg.ExplainAction(hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": "git push origin main"},
},
Scope: hitl.ActionScope{
ProjectDir: "/proj",
},
}, settings.TierIrreversible)
	if got.Key != approvals.KeyCommandDestructive {
		t.Fatalf("command push key: got %q", got.Key)
	}
	if got.UsedFallback || got.Copy.What == "" {
		t.Fatalf("expected registry-rendered copy, got %+v", got)
	}
}
