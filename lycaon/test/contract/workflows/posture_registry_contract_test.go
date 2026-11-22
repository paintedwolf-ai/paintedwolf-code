package contract

import (
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/rules"
	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	"github.com/lycaon/lycaon/internal/session/profiles"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestPostureRegistryClosure(t *testing.T) {
	t.Parallel()
	reg, err := profiles.LoadPostureRegistry()
	contractcheck.FailErr(t, "profiles.LoadPostureRegistry failed", err)

	want := sessionposture.AllSessionPostures()
	got := reg.List()
	if len(got) != len(want) {
		t.Fatalf("registry has %d postures want %d", len(got), len(want))
	}
	seen := map[api.SessionPosture]struct{}{}
	for _, spec := range got {
		seen[spec.ID] = struct{}{}
		if len(spec.Rules) == 0 {
			t.Fatalf("posture %q missing rules[]", spec.ID)
		}
		for _, rulePath := range spec.Rules {
			if _, err := rules.LoadRulesConfig(config.Rel(rules.NormalizeRulesPath(rulePath))); err != nil {
				t.Fatalf("posture %q rules path %q: %v", spec.ID, rulePath, err)
			}
		}
	}
	for _, p := range want {
		if _, ok := seen[p]; !ok {
			t.Fatalf("registry missing posture %q", p)
		}
	}
}

func TestComposePolicyPosturesValid(t *testing.T) {
	t.Parallel()
	policy, err := workflowcomposition.LoadComposePolicy()
	contractcheck.FailErr(t, "workflowcomposition.LoadComposePolicy failed", err)
	if len(policy.PostureRequiredGates) == 0 {
		t.Fatal("compose policy loaded no posture_required_gates")
	}
	for posture, gates := range policy.PostureRequiredGates {
		if !sessionposture.ValidSessionPosture(posture) {
			t.Errorf("compose policy keys unknown posture %q", posture)
		}
		if len(gates) == 0 {
			t.Errorf("compose policy posture %q has no gates", posture)
		}
	}
	if _, ok := policy.PostureRequiredGates[string(api.SessionPostureSpec)]; ok {
		t.Error("spec posture carries compose-time required gates")
	}
}
