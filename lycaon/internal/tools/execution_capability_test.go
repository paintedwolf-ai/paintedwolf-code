package tools

import (
	"github.com/lycaon/lycaon/internal/capabilityrequest"

	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
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
			request, reject := capabilityrequest.ParseCapabilityRequest(args)
			if reject != nil || request == nil {
				t.Fatalf("parse %s: %v", name, reject)
			}
			if reject := capabilityrequest.ValidateCapabilityContract(commandContract, args); reject != nil {
				t.Fatalf("command %s: %v", name, reject)
			}
			if reject := capabilityrequest.ValidateCapabilityContract(terminalContract, args); reject == nil {
				t.Fatalf("held session accepted %s", name)
			}
		}
		for _, value := range []any{false, nil, "true", map[string]any{"extra": true}} {
			if _, reject := capabilityrequest.ParseCapabilityRequest(map[string]any{"capability_request": map[string]any{name: value}}); reject == nil {
				t.Fatalf("accepted invalid %s: %#v", name, value)
			}
		}
	}
	if _, reject := capabilityrequest.ParseCapabilityRequest(map[string]any{"capability_request": map[string]any{"host_execution": true, "direct_ip": true}}); reject == nil {
		t.Fatal("accepted conflicting authority")
	}
}

func TestExecutionPermitCannotMoveBetweenProgramChildren(t *testing.T) {
	req := confine.Request{HostExecution: true}
	args := map[string]any{"argv": []string{"fixture-program", "same-operand"}}
	tc := ToolContext{Identity: InvocationIdentity{SessionID: "session", ToolCallID: "actual-model-call"}, Invocation: Invocation{ID: "first-child"}, Execution: InvocationExecution{HostExecution: true}, Effects: InvocationEffects{CanonicalArgs: args}}
	action := hitl.ProposedAction{Execution: hitl.ActionExecution{ExecutionBoundaryDigest: ExecutionBoundaryDigest(req)}}
	tc.StampExecutionApproval(args, action)
	other := tc
	other.Invocation.ID = "second-child"
	if reject := finalizeExecutionCapability(other, req); reject == nil {
		t.Fatal("identical operands and outer model call reused another child's permit")
	}
	if reject := finalizeExecutionCapability(tc, req); reject != nil {
		t.Fatalf("original child lost its permit: %v", reject)
	}
	if tc.Identity.ToolCallID != "actual-model-call" {
		t.Fatal("child identity replaced actual model call correlation")
	}
}

func TestExecutionPermitBindsBoundaryAndIsSingleUse(t *testing.T) {
	req := confine.Request{ProcessControl: true, Roots: []string{"/tmp/project"}}
	tc := ToolContext{
		Identity: InvocationIdentity{SessionID: "task",
			ToolCallID: "call"},
		Execution: InvocationExecution{ProcessControl: true},
	}
	permit := func() *executionPermit {
		return &executionPermit{session: tc.Identity.SessionID, call: tc.Identity.ToolCallID, processControl: true, boundary: ExecutionBoundaryDigest(req), arguments: executionArgumentsDigest(tc.Effects.CanonicalArgs)}
	}
	tc.Execution.executionPermit = permit()
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
	tc.Execution.executionPermit = permit()
	tc.Identity.ToolCallID = "other"
	if reject := finalizeExecutionCapability(tc, req); reject == nil {
		t.Fatal("cross-call permit accepted")
	}
	tc.Execution.executionPermit = nil
	if reject := finalizeExecutionCapability(tc, req); reject == nil {
		t.Fatal("missing permit accepted")
	}
}

func TestExecutionBoundaryIdentityTreatsGrantsAsSets(t *testing.T) {
	first := confine.Request{ProcessControl: true, Roots: []string{"/project"}, GrantedWriteRoots: []string{"/cache", "/output"}, LocalListen: true, LocalListenPorts: []uint16{8000, 9000}}
	replay := first
	replay.GrantedWriteRoots = []string{"/output", "/cache", "/cache"}
	replay.LocalListenPorts = []uint16{9000, 8000}
	if ExecutionBoundaryDigest(first) != ExecutionBoundaryDigest(replay) {
		t.Fatal("reordered grants changed the reviewed boundary")
	}
	replay.LocalListenPorts = []uint16{8000, 9001}
	if ExecutionBoundaryDigest(first) == ExecutionBoundaryDigest(replay) {
		t.Fatal("changed port reused the reviewed boundary")
	}
}
