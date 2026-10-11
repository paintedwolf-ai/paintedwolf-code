package profiles

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/sandbox"
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
