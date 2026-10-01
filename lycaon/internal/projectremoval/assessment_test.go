package projectremoval

import (
	"testing"

	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
)

func TestDeviceReferencesRetainCandidate(t *testing.T) {
	tests := []struct {
		name  string
		state extensionstate.RemovalState
		code  string
	}{
		{"dependency", extensionstate.RemovalState{Lock: extpacks.LockFile{Packages: []extpacks.LockedPackage{{ID: "acme/parent", Dependencies: map[string]string{"acme/one": "*"}}}}}, "extension_dependency"},
		{"selection", extensionstate.RemovalState{Desired: extpacks.DesiredState{Own: map[string]string{"command": "acme/one"}}}, "device_selection"},
		{"configuration", extensionstate.RemovalState{Desired: extpacks.DesiredState{Configuration: map[string]map[string]any{"acme/one": {"depth": 1}}}}, "device_configuration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := assessPack("acme/one", tt.state, evidence{Complete: true})
			if row.Disposition != "retained" || len(row.Reasons) != 1 || row.Reasons[0].Code != tt.code {
				t.Fatalf("assessment = %+v", row)
			}
		})
	}
}
