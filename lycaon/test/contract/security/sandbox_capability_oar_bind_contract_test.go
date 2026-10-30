package contract

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// [OAR-EVAL-1] Intrinsic refusals are selected by their typed identity,
// not by the overlapping observation tokens from an earlier phase.
func TestSandboxCapabilityRulesBindActualRefusals(t *testing.T) {
	pipeline := stockRejectionPipeline(t)
	checked := 0
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "load actual tool identities", err)
	var toolNames []string
	for tool := range schemas.Tools {
		toolNames = append(toolNames, tool)
	}
	slices.Sort(toolNames)
	loader, err := oar.NewLoader(filepath.Join(contractcheck.RepoRoot(t), "schemas"))
	contractcheck.FailErr(t, "create policy loader", err)
	rules, err := loader.LoadEffectivePolicy()
	contractcheck.FailErr(t, "load stock policy", err)
	for _, rule := range rules.All() {
		if !strings.HasPrefix(rule.ID, "SANDBOX_") || rule.Anchor != oar.AnchorToolRejected {
			continue
		}
		selected := append(append([]string(nil), rule.Selector["tool"]...), rule.Selector["paintedwolf.operation"]...)
		// Intrinsic code-only rules apply regardless of tool identity. Exercise
		// every actual tool rather than skipping an unrestricted selector.
		if len(rule.Selector) == 0 {
			selected = toolNames
		}
		if len(selected) == 0 {
			t.Fatalf("sandbox refusal %s has no exercised tool identities", rule.ID)
		}
		for _, tool := range selected {
			assertIntrinsicRejection(t, pipeline, tool, rule.ID)
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no sandbox refusals exercised")
	}
}

func stockRejectionPipeline(t *testing.T) *oar.GuardPipeline {
	t.Helper()
	contractcheck.FailErr(t, "install anchors", anchorcatalog.InstallBundled())
	loader, err := oar.NewLoader(filepath.Join(contractcheck.RepoRoot(t), "schemas"))
	contractcheck.FailErr(t, "create policy loader", err)
	rules, err := loader.LoadEffectivePolicy()
	contractcheck.FailErr(t, "load stock policy", err)
	pipeline := oar.NewGuardPipeline(rules, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorToolRejected)
	return pipeline
}

func assertIntrinsicRejection(t *testing.T, pipeline *oar.GuardPipeline, tool, code string) {
	t.Helper()
	for _, observed := range []string{code, "UNRELATED_REFUSAL"} {
		gc := oar.NewGuardContext()
		gc.Session.SessionID = t.Name() + tool + observed
		gc.Invocation.Tool = tool
		gc.ObservedRejectCode = observed
		result, err := pipeline.EvaluateBlock(t.Context(), oar.AnchorToolRejected, gc)
		contractcheck.FailErr(t, "evaluate intrinsic refusal", err)
		if observed == code {
			if result == nil || result.Decision == nil || result.Decision.Code != code || len(result.Decision.Copy) != 5 {
				t.Fatalf("%s on %s lost policy: %+v", code, tool, result)
			}
		} else if result != nil && result.Decision != nil && result.Decision.Code == code {
			t.Fatalf("unrelated refusal on %s selected %s", tool, code)
		}
	}
}
