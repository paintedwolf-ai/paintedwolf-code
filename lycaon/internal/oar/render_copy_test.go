package oar

import (
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
