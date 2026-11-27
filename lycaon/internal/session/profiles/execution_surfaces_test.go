package profiles

import (
	"reflect"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/sandbox"
)

func TestReachFollowsEffectiveToolProfile(t *testing.T) {
	t.Parallel()

	exec := []hostresources.ExecutionSurface{hostresources.SurfaceProcessExec}
	tests := []struct {
		name          string
		profile       sandbox.ToolProfile
		surfaces      []hostresources.ExecutionSurface
		hostExecution bool
	}{
		{name: "read only", profile: sandbox.ToolProfile{Tools: map[string]bool{"read": true}}},
		{name: "command", profile: sandbox.ToolProfile{Tools: map[string]bool{"command": true}}, surfaces: exec, hostExecution: true},
		{name: "terminal", profile: sandbox.ToolProfile{Tools: map[string]bool{"terminal_open": true}}, surfaces: exec},
		{name: "denied", profile: sandbox.ToolProfile{Tools: map[string]bool{"command": true}, DenyTools: []string{"command"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reach := ReachForToolProfile(tt.profile, hostresources.RoleWorker, false)
			if !reflect.DeepEqual(reach.Surfaces, tt.surfaces) {
				t.Fatalf("surfaces = %#v, want %#v", reach.Surfaces, tt.surfaces)
			}
			if got := slices.Contains(reach.Offered, "host_execution"); got != tt.hostExecution {
				t.Fatalf("host_execution offered = %v, want %v (offered %v)", got, tt.hostExecution, reach.Offered)
			}
			if reach.Role != hostresources.RoleWorker || reach.Unconfined {
				t.Fatalf("reach = %+v", reach)
			}
		})
	}
}
