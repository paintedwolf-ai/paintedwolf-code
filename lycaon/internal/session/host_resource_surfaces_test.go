package session

import (
	"context"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestExecutionSurfacesFollowEffectiveToolProfile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		profile sandbox.ToolProfile
		want    []hostresources.ExecutionSurface
	}{
		{name: "read only", profile: sandbox.ToolProfile{Tools: map[string]bool{"read": true}}, want: []hostresources.ExecutionSurface{}},
		{name: "command", profile: sandbox.ToolProfile{Tools: map[string]bool{"command": true}}, want: []hostresources.ExecutionSurface{hostresources.SurfaceProcessExec}},
		{name: "terminal", profile: sandbox.ToolProfile{Tools: map[string]bool{"terminal_open": true}}, want: []hostresources.ExecutionSurface{hostresources.SurfaceProcessExec}},
		{name: "denied", profile: sandbox.ToolProfile{Tools: map[string]bool{"command": true}, DenyTools: []string{"command"}}, want: []hostresources.ExecutionSurface{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := executionSurfacesForToolProfile(tt.profile); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("surfaces = %#v, want %#v", got, tt.want)
			}
		})
	}
}

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
	m.SetAgentRegistry(reg)
	got := m.skillSelectorForSession(context.Background(), &api.Session{AgentType: "curated-worker"})
	want := skills.Selector{Names: []string{"verify-a-change"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("skill selector = %#v, want %#v", got, want)
	}
}
