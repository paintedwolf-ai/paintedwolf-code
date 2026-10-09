package contract

import (
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/session"
	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"os"
	"path/filepath"
	"testing"
)

func TestPostureRegistryClosure(t *testing.T) {
	t.Parallel()
	reg, err := session.LoadPostureRegistry()
	contractcheck.FailErr(t, "session.LoadPostureRegistry failed", err)

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
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "host", "compose-policy.yaml")
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read file", err)
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		contractcheck.FailErr(t, "unmarshal YAML document", err)
	}
	if _, ok := raw["mode_required_gates"]; ok {
		t.Fatal("compose-policy must not use mode_required_gates key")
	}
	gatesRaw, ok := raw["posture_required_gates"].(map[string]any)
	if !ok {
		t.Fatal("compose-policy.yaml missing posture_required_gates")
	}
	for posture, gates := range gatesRaw {
		if !sessionposture.ValidSessionPosture(posture) {
			t.Fatalf("compose-policy unknown posture %q", posture)
		}
		list, ok := gates.([]any)
		if !ok || len(list) == 0 {
			t.Fatalf("compose-policy posture %q has empty gates", posture)
		}
	}
	if _, ok := gatesRaw["spec"]; ok {
		t.Fatal("spec posture should not have compose-time required gates")
	}
}
