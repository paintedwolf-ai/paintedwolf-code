package orchestration

import (
	"context"
	"github.com/lycaon/lycaon/internal/agentdef"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBundledAgentSkillSelectorsResolve(t *testing.T) {
	reg := NewMemoryAgentRegistry()
	testutil.FailErr(t, "LoadRequiredAgentRegistry", LoadRequiredAgentRegistry(context.Background(), reg))
	content, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "DiscoverStockContent", err)
	effective := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs: content, Desired: extpacks.EmptyDesired(),
	})
	loaded, diags := extpacks.LoadEffectiveSkills(effective)
	if len(diags) > 0 {
		t.Fatalf("skill catalog diagnostic: %+v", diags[0])
	}
	knownSkills := make(map[string]struct{}, len(loaded))
	for _, skill := range loaded {
		knownSkills[skill.Name] = struct{}{}
	}
	for _, agent := range reg.List() {
		if agent.Skills.Empty() {
			continue
		}
		for _, name := range agent.Skills.Names {
			if _, ok := knownSkills[name]; !ok {
				t.Fatalf("agent %q selects missing bundled skill %q", agent.ID, name)
			}
		}
	}
}

func TestBundledCoordinatorAndImplementerUseAllSkills(t *testing.T) {
	reg := NewMemoryAgentRegistry()
	testutil.FailErr(t, "LoadRequiredAgentRegistry", LoadRequiredAgentRegistry(context.Background(), reg))
	content, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "DiscoverStockContent", err)
	effective := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs: content, Desired: extpacks.EmptyDesired(),
	})
	loaded, diags := extpacks.LoadEffectiveSkills(effective)
	if len(diags) > 0 {
		t.Fatalf("skill catalog diagnostic: %+v", diags[0])
	}
	for _, skill := range loaded {
		if skill.Description == "" {
			t.Fatalf("skill %q has no discovery description", skill.Name)
		}
	}

	coord, coordErr := reg.Get(ProfileCoordinator)
	testutil.FailErr(t, "Get coordinator", coordErr)
	impl, implErr := reg.Get(ProfileImplementer)
	testutil.FailErr(t, "Get implementer", implErr)
	for _, profile := range []agentdef.Profile{coord, impl} {
		if !profile.Skills.All || len(profile.Skills.Names) != 0 {
			t.Fatalf("agent %q skills = %#v, want all", profile.ID, profile.Skills)
		}
	}
}
