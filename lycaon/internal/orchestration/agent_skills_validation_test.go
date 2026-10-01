package orchestration

import (
	"github.com/lycaon/lycaon/internal/agentdef"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
)

func TestValidateAgentSkillSurface(t *testing.T) {
	t.Parallel()
	profiles := map[string]sandbox.ToolProfile{
		"reader":    {ID: "reader", Tools: map[string]bool{SkillsReadTool: true}},
		"no-reader": {ID: "no-reader", Tools: map[string]bool{"read": true}},
	}

	tests := []struct {
		name    string
		profile agentdef.Profile
		wantErr string
	}{
		{
			name: "selected skills need a profile that grants the reader",
			profile: agentdef.Profile{
				ID: "fixture-agent", ToolProfile: "no-reader", Skills: skills.Selector{All: true},
			},
			wantErr: "does not grant skills_read",
		},
		{
			name: "profile granting the reader without selected skills is fine",
			profile: agentdef.Profile{
				ID: "fixture-agent", ToolProfile: "reader",
			},
		},
		{
			name: "aligned",
			profile: agentdef.Profile{
				ID: "fixture-agent", ToolProfile: "reader",
				Skills: skills.Selector{Names: []string{"verify-a-change"}},
			},
		},
		{
			name: "unknown profile is named, not skipped",
			profile: agentdef.Profile{
				ID: "fixture-agent", ToolProfile: "absent", Skills: skills.Selector{All: true},
			},
			wantErr: `tool_profile "absent" not found`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reg := NewMemoryAgentRegistry()
			if err := reg.Register(tt.profile); err != nil {
				t.Fatalf("Register: %v", err)
			}
			err := ValidateAgentSkillSurface(reg, profiles)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateAgentSkillSurface: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}
