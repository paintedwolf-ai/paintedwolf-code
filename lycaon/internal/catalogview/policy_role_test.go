package catalogview_test

import (
	"github.com/lycaon/lycaon/internal/configlayout"
	"log/slog"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/testutil"
)

const invocationRule = `oar: '1.0'
id: ACME_NEW_CODE
kind: policy
anchor: tool.pre_invoke
selector:
  tool:
    - write
requires:
  profiles:
    - tool
when: 'true'
effect: block
copy:
  what: A write happened.
  cause: Because.
  why: Reasons.
  fix: Do something else.
  instead: Branch on Code
`

func TestProf10NewPolicyIdentityAtPreInvokeLoads(t *testing.T) {
	pack := fixturePack(t, "acme/guard", map[string]string{"policy/arbitrary-name.yaml": invocationRule})
	eff := resolveWithFixtures(t, pack)
	if _, err := catalogview.Build(t.Context(), configlayout.FindModuleRoot(), eff); err != nil {
		t.Fatalf("[OAR-PROF-10] pre-invocation policy must load: %v", err)
	}
}

func TestPolicyRuleAtAGatingAnchorLoads(t *testing.T) {
	pack := fixturePack(t, "acme/guard", map[string]string{
		"policy/ACME_POST.yaml": strings.Replace(
			strings.Replace(invocationRule, "anchor: tool.pre_invoke", "anchor: tool.post_invoke", 1),
			"id: ACME_NEW_CODE", "id: ACME_POST", 1),
	})
	eff := resolveWithFixtures(t, pack)
	if _, err := catalogview.Build(t.Context(), configlayout.FindModuleRoot(), eff); err != nil {
		t.Fatalf("a rule at a gating anchor must load: %v", err)
	}
}

func TestProf10UnsupportedTransformIsolatesOnlyItsPack(t *testing.T) {
	broken := fixturePack(t, "acme/guard", map[string]string{"policy/ACME_NEW_CODE.yaml": strings.Replace(invocationRule, "effect: block", "effect: transform\ntransform:\n  action: replace\n  target: content\n  replacement: replacement", 1)})
	fine := fixturePack(t, "acme/fine", map[string]string{
		"contributions/commands/ok.yaml": "id: acme/fine:ok\ntitle: OK\naction:\n  kind: navigate\n  destination: home\n",
	})
	eff := resolveWithFixtures(t, broken, fine)
	cache := catalogview.NewCache(configlayout.FindModuleRoot(), slog.New(slog.DiscardHandler))
	_, committed, err := cache.ForCommitted(t.Context(), eff)
	testutil.FailErr(t, "ForCommitted", err)
	if !heldOut(committed, "acme/guard") {
		t.Fatalf("acme/guard should be held out and named: %+v", committed.Diagnostics)
	}
}
