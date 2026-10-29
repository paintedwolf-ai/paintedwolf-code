package settings

import (
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
)

func TestCurrentActionCoverageIsExactAndDirectional(t *testing.T) {
	path := grantedpath.Normalize(t.TempDir() + "/approved.txt")
	for _, tc := range []struct {
		name    string
		access  []hitl.GrantedPathDelta
		target  gate.FileTarget
		covered bool
	}{
		{"read", []hitl.GrantedPathDelta{{Path: path}}, gate.FileTarget{Path: path, Mode: gate.ModeRead}, true},
		{"write", []hitl.GrantedPathDelta{{Path: path, Write: true}}, gate.FileTarget{Path: path, Mode: gate.ModeWrite}, true},
		{"write covers read", []hitl.GrantedPathDelta{{Path: path, Write: true}}, gate.FileTarget{Path: path, Mode: gate.ModeRead}, true},
		{"read refuses write", []hitl.GrantedPathDelta{{Path: path}}, gate.FileTarget{Path: path, Mode: gate.ModeWrite}, false},
		{"different path", []hitl.GrantedPathDelta{{Path: path, Write: true}}, gate.FileTarget{Path: path + ".other", Mode: gate.ModeWrite}, false},
		{"no tree authority", []hitl.GrantedPathDelta{{Path: path, Write: true, Tree: true}}, gate.FileTarget{Path: path, Mode: gate.ModeWrite}, false},
		{"absent", nil, gate.FileTarget{Path: path, Mode: gate.ModeWrite}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			action := hitl.ProposedAction{Invocation: hitl.ActionInvocation{ApprovedFileAccess: tc.access}}
			if got := currentActionCoversTarget(action, &tc.target); got != tc.covered {
				t.Fatalf("exact action coverage = %v, want %v", got, tc.covered)
			}
		})
	}
}
