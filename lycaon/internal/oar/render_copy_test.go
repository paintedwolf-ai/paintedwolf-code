package oar

import (
	"maps"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// [OAR-COPY-1] Frozen copy is delivered without changing an advisory into a refusal.
func TestRendererFrozenCopyKeepsAdvisoryDisposition(t *testing.T) {
	setupAuthorCheckRenderer(t)
	for _, effect := range []Effect{EffectBlock, EffectWarn, EffectNudge} {
		t.Run(string(effect), func(t *testing.T) {
			copy := map[string]string{"what": "Observed state", "cause": "Measured cause", "why": "Policy invariant", "fix": "Reachable recovery", "instead": "Bounded next action"}
			d := &Decision{Effect: effect, Code: "FIXTURE", Copy: copy}
			if effect != EffectBlock {
				d.Advisories = []Advisory{{Code: d.Code, Copy: copy}}
			}
			rendered, err := NewRenderer(nil, nil).Render(t.Context(), StagePostTool, d)
			testutil.FailErr(t, "render frozen decision", err)
			if len(rendered) != 1 {
				t.Fatalf("render count = %d", len(rendered))
			}
			text := rendered[0].Text
			if strings.Contains(text, "Rejected:") != (effect == EffectBlock) {
				t.Fatalf("wrong %s disposition: %s", effect, text)
			}
			for _, value := range copy {
				if !strings.Contains(text, value) {
					t.Fatalf("lost frozen copy member %q: %s", value, text)
				}
			}
		})
	}
}

func TestHostPresentationPreservesOmittedAndEmptyCopyMembers(t *testing.T) {
	setupAuthorCheckRenderer(t)
	for _, copy := range []map[string]string{nil, {}, {"what": "", "fix": ""}} {
		before := maps.Clone(copy)
		decision := &Decision{Code: "FIXTURE", Effect: EffectBlock, Copy: copy}
		rendered, err := NewRenderer(nil, nil).Render(t.Context(), StagePostTool, decision)
		testutil.FailErr(t, "render code-only outcome", err)
		if len(rendered) != 1 || (!maps.Equal(decision.Copy, before) || (decision.Copy == nil) != (before == nil)) {
			t.Fatalf("host presentation altered canonical copy: %#v", decision.Copy)
		}
		if strings.Contains(rendered[0].Text, "Fix:") || strings.Contains(rendered[0].Text, "Next:") || (copy != nil && !strings.Contains(rendered[0].Text, "FIXTURE")) {
			t.Fatalf("code-only outcome acquired a remedy: %s", rendered[0].Text)
		}
	}
}

func TestEvaluatedRuleWithoutCopyDoesNotConsultFallbackCatalog(t *testing.T) {
	setupAuthorCheckRenderer(t)
	rule := &Rule{ID: "FIXTURE", Effect: EffectBlock}
	decision := decisionFromRule(rule, NewGuardContext(), nil)
	if decision.Copy == nil || len(decision.Copy) != 0 {
		t.Fatalf("omitted rule copy was not frozen: %#v", decision.Copy)
	}
	rendered, err := NewRenderer(stubFmt{}, stubFmt{}).Render(t.Context(), StagePostTool, &decision)
	testutil.FailErr(t, "render omitted rule copy", err)
	if len(rendered) != 1 || !strings.Contains(rendered[0].Text, "Code: FIXTURE") || rendered[0].Text == "Rejected:\nCode: FIXTURE" || strings.Contains(rendered[0].Text, "Fix:") {
		t.Fatalf("unexpected code-only presentation: %#v", rendered)
	}
}
