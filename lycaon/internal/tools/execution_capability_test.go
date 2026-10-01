package tools

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/packageexec"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestExecutionCapabilityGrammar(t *testing.T) {
	commandContract, commandOK := toolcontract.Lookup("command")
	terminalContract, terminalOK := toolcontract.Lookup("terminal_open")
	if !commandOK || !terminalOK {
		t.Fatal("required execution contracts are missing")
	}
	for _, name := range []string{"process_control", "host_execution"} {
		for _, value := range []any{true, map[string]any{}} {
			args := map[string]any{"capability_request": map[string]any{name: value}}
			request, reject := ParseCapabilityRequest(args)
			if reject != nil || request == nil {
				t.Fatalf("parse %s: %v", name, reject)
			}
			if reject := ValidateCapabilityContract(commandContract, args); reject != nil {
				t.Fatalf("command %s: %v", name, reject)
			}
			if reject := ValidateCapabilityContract(terminalContract, args); reject == nil {
				t.Fatalf("held session accepted %s", name)
			}
		}
		for _, value := range []any{false, nil, "true", map[string]any{"extra": true}} {
			if _, reject := ParseCapabilityRequest(map[string]any{"capability_request": map[string]any{name: value}}); reject == nil {
				t.Fatalf("accepted invalid %s: %#v", name, value)
			}
		}
	}
	if _, reject := ParseCapabilityRequest(map[string]any{"capability_request": map[string]any{"host_execution": true, "direct_ip": true}}); reject == nil {
		t.Fatal("accepted conflicting authority")
	}
}

func TestExecutionPermitBindsBoundaryAndIsSingleUse(t *testing.T) {
	req := confine.Request{ProcessControl: true, Roots: []string{"/tmp/project"}}
	tc := ToolContext{SessionID: "task", ToolCallID: "call", ProcessControl: true}
	permit := func() *executionPermit {
		return &executionPermit{session: tc.SessionID, call: tc.ToolCallID, processControl: true, boundary: executionBoundaryDigest(req), arguments: executionArgumentsDigest(tc.CanonicalArgs)}
	}
	tc.executionPermit = permit()
	changed := req
	changed.Roots = []string{"/tmp/other"}
	if reject := finalizeExecutionCapability(tc, changed); reject == nil {
		t.Fatal("changed roots accepted")
	}
	if reject := finalizeExecutionCapability(tc, req); reject != nil {
		t.Fatalf("matching permit: %v", reject)
	}
	if reject := finalizeExecutionCapability(tc, req); reject == nil {
		t.Fatal("permit reused")
	}
	tc.executionPermit = permit()
	tc.ToolCallID = "other"
	if reject := finalizeExecutionCapability(tc, req); reject == nil {
		t.Fatal("cross-call permit accepted")
	}
	tc.executionPermit = nil
	if reject := finalizeExecutionCapability(tc, req); reject == nil {
		t.Fatal("missing permit accepted")
	}
}

func TestExecutionBoundaryIdentityTreatsGrantsAsSets(t *testing.T) {
	first := confine.Request{ProcessControl: true, Roots: []string{"/project"}, GrantedWriteRoots: []string{"/cache", "/output"}, LocalListen: true, LocalListenPorts: []uint16{8000, 9000}}
	replay := first
	replay.GrantedWriteRoots = []string{"/output", "/cache", "/cache"}
	replay.LocalListenPorts = []uint16{9000, 8000}
	if executionBoundaryDigest(first) != executionBoundaryDigest(replay) {
		t.Fatal("reordered grants changed the reviewed boundary")
	}
	replay.LocalListenPorts = []uint16{8000, 9001}
	if executionBoundaryDigest(first) == executionBoundaryDigest(replay) {
		t.Fatal("changed port reused the reviewed boundary")
	}
}

func TestExecutionReviewPreservesPackageAndPolicyFacts(t *testing.T) {
	root := t.TempDir()
	execution := &packageexec.Execution{Manager: "npm"}
	tc := ToolContext{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}, ActiveRootID: "root", ProcessControl: true, PackageExecution: execution, PolicyWriteGrants: []confine.ProtectedPathGrant{confine.NewProtectedPathGrant(filepath.Join(root, "AGENTS.md"))}}
	executor := NewDefaultToolExecutor(nil, nil, "implement")
	action := executor.executionCapabilityAction(t.Context(), "command", map[string]any{"command": "example"}, tc)
	if action.PackageExecution != execution || len(action.AgentPolicy) != 1 {
		t.Fatalf("execution capability dropped independent review facts: %+v", action)
	}
}
