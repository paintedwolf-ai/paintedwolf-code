package settings

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/pkg/api"
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

func TestCurrentActionFileApprovalPreservesIndependentGates(t *testing.T) {
	approvals := ladderGate(t)
	path := grantedpath.Normalize(t.TempDir() + "/approved.txt")
	for _, tc := range []struct {
		name      string
		tool      string
		contained hitl.Contained
		layers    ApprovalRuleLayers
		want      api.ApprovalGate
	}{
		{name: "user rule", tool: "read", layers: ApprovalRuleLayers{Device: []ApprovalRule{{
			Category: ApprovalCategoryTool, Pattern: "read", Effect: ApprovalEffectAsk,
		}}}, want: api.GateUserRule},
		{name: "MCP", tool: "mcp_docs_search_docs", want: api.GateMCPUnleased},
		{name: "direct IP", tool: "read", contained: hitl.Contained{DirectIP: true}, want: api.GateUnobservedChannel},
		{name: "socket", tool: "read", contained: hitl.Contained{SocketCount: 1, SocketPathsDigest: "unapproved"}, want: api.GateUnobservedChannel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			action := hitl.ProposedAction{
				Invocation: hitl.ActionInvocation{Tool: tc.tool, Files: []string{path},
					ApprovedFileAccess: []hitl.GrantedPathDelta{{Path: path}}},
				Execution: hitl.ActionExecution{Contained: tc.contained},
			}
			facts, _, _ := approvals.factsForAction(action, ApprovalConfig{}, tc.layers)
			if !facts.FileLeased || facts.Leased || facts.LeasedExact || facts.ExecutionCapabilityLeased {
				t.Fatalf("file authority crossed gate boundaries: %+v", facts)
			}
			verdict, decision := gate.Evaluate(facts, gate.PostureStrict)
			if verdict != gate.Ask || !slices.Contains(decision.Gates(), tc.want) {
				t.Fatalf("file approval suppressed %s: %+v", tc.want, decision)
			}
		})
	}
}

func TestCurrentActionFileApprovalSelectsUncoveredStagedWrite(t *testing.T) {
	approvals := ladderGate(t)
	read := grantedpath.Normalize(t.TempDir() + "/source.sed")
	write := grantedpath.Normalize(t.TempDir() + "/output.txt")
	action := hitl.ProposedAction{Invocation: hitl.ActionInvocation{
		Tool: "command", Files: []string{read, write},
		NativePathTargets:  []hitl.NativePathTarget{{Path: read}, {Path: write, Write: true}},
		ApprovedFileAccess: []hitl.GrantedPathDelta{{Path: read}},
	}}
	facts, _, _ := approvals.factsForAction(action, ApprovalConfig{}, ApprovalRuleLayers{})
	if facts.FileLeased || facts.File == nil || facts.File.Path != write || facts.File.Mode != gate.ModeWrite {
		t.Fatalf("staged disclosure lost uncovered write: %+v", facts)
	}
	if approvals.fileAccessCoversTargets(nil, hitl.ProposedAction{}) {
		t.Fatal("absent file subjects became authority")
	}
}
