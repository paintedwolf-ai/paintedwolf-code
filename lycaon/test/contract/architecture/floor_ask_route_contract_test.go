package contract

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolcontract"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// refusedGrantFact is the host fact that names each capability's grant for a
// path the applied profile refused.
var refusedGrantFact = map[string]string{
	"write_root": "paintedwolf.refused_write_grants",
	"read_path":  "paintedwolf.refused_read_grants",
}

// Nothing blocks without an ask outside the control plane: every filesystem
// layer the sandbox can refuse at either names a capability whose declaration
// asks, with a stock rule that tells the agent to retry through it, or is the
// control plane.
func TestEveryFloorLayerReachesAnAskOrIsTheControlPlane(t *testing.T) {
	t.Parallel()
	rules := stockPostInvokeRules(t)
	for _, layer := range confine.FloorLayers() {
		recovery := layer.Recovery()
		if recovery.Terminal() {
			if layer != confine.FloorControlPlane && layer != confine.FloorReadControlPlane {
				t.Errorf("%s refuses without an ask route and is not the control plane", layer)
			}
			continue
		}
		if !toolcontract.IsCapabilityRequestField(recovery.Capability) {
			t.Errorf("%s recovers through %q, which no tool can declare", layer, recovery.Capability)
			continue
		}
		fact, ok := refusedGrantFact[recovery.Capability]
		if !ok {
			t.Errorf("%s recovers through %q, which no refused-path fact reports", layer, recovery.Capability)
			continue
		}
		if code := retryRuleReading(rules, fact); code == "" {
			t.Errorf("%s: no stock post-invoke rule reads %s and registers as a retry outcome", layer, fact)
		}
	}
}

// Git index changes the sandbox kept out of the worktree reach a retry rule.
func TestWorktreeFactsReachARetryRule(t *testing.T) {
	t.Parallel()
	rules := stockPostInvokeRules(t)
	for _, fact := range []string{
		"paintedwolf.worktree_stale_paths",
		"paintedwolf.worktree_leftover_paths",
		"paintedwolf.worktree_conflict_paths",
	} {
		if code := retryRuleReading(rules, fact); code == "" {
			t.Errorf("no stock post-invoke rule reads %s and registers as a retry outcome", fact)
		}
	}
}

// FloorLayers must list every declared layer, or a new layer would escape
// the route check above.
func TestFloorLayersListsEveryDeclaredLayer(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	corpus, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon", "internal", "confine"))
	contractcheck.FailErr(t, "load confine corpus", err)
	listed := confine.FloorLayers()
	found := 0
	for _, file := range corpus.Files() {
		if file.IsTest {
			continue
		}
		ast.Inspect(file.AST, func(node ast.Node) bool {
			spec, ok := node.(*ast.ValueSpec)
			if !ok {
				return true
			}
			typ, ok := spec.Type.(*ast.Ident)
			if !ok || typ.Name != "FloorLayer" {
				return true
			}
			for _, value := range spec.Values {
				lit, ok := value.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				layer, unquoteErr := strconv.Unquote(lit.Value)
				if unquoteErr != nil {
					continue
				}
				found++
				if !slices.Contains(listed, confine.FloorLayer(layer)) {
					t.Errorf("layer %q is declared but missing from FloorLayers", layer)
				}
			}
			return true
		})
	}
	if found == 0 {
		t.Fatal("no FloorLayer constants found; the corpus scan is stale")
	}
}

func stockPostInvokeRules(t *testing.T) []*oar.Rule {
	t.Helper()
	contractcheck.FailErr(t, "install anchors", anchorcatalog.InstallBundled())
	loader, err := oar.NewLoader(filepath.Join(contractcheck.RepoRoot(t), "schemas"))
	contractcheck.FailErr(t, "create policy loader", err)
	rules, err := loader.LoadEffectivePolicy()
	contractcheck.FailErr(t, "load stock policy", err)
	var out []*oar.Rule
	for _, rule := range rules.All() {
		if rule.Anchor == oar.AnchorToolPost {
			out = append(out, rule)
		}
	}
	return out
}

// retryRuleReading returns a rule whose condition reads fact and whose code is
// a registered retry isolation outcome.
func retryRuleReading(rules []*oar.Rule, fact string) string {
	for _, rule := range rules {
		if !slices.Contains(oar.FactsReferenced(rule.When, rule.Flow), fact) {
			continue
		}
		if outcome, ok := isolation.Lookup(rule.ID); ok && outcome.Disposition == isolation.DispositionRetry {
			return rule.ID
		}
	}
	return ""
}
