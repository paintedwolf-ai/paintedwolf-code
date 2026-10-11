package session

import (
	"context"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSkillSelectorForSessionUsesAgentDefinition(t *testing.T) {
	t.Parallel()
	reg := orchestration.NewMemoryAgentRegistry()
	if err := reg.Register(agentdef.Profile{
		ID:     "curated-worker",
		Skills: skills.Selector{Names: []string{"verify-a-change"}},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	m := newSkillsTestManager(t)
	m.Profiles.SetAgentRegistry(reg)
	got := m.Profiles.SkillSelector(context.Background(), &api.Session{AgentType: "curated-worker"})
	want := skills.Selector{Names: []string{"verify-a-change"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("skill selector = %#v, want %#v", got, want)
	}
}
