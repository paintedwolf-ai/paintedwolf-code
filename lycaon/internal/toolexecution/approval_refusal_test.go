package toolexecution

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestApprovalRefusalRejectKeepsDeniedCodesForPeopleOnly(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cause      isolation.PathDenialCause
		resolvedBy string
		code       string
		fact       string
	}{
		{"person declined", isolation.PathDenialUserDeclined, "", isolation.CodeLoopbackConnectDenied, ""},
		{"person declined earlier", isolation.PathDenialPriorUserDeclined, "", isolation.CodeLoopbackConnectDenied, ""},
		{"policy", isolation.PathDenialNonHumanRejected, authzledger.ResolvedByPolicy, isolation.CodeApprovalUnavailable, "approval_refused_automatically"},
		{"system deny", isolation.PathDenialNonHumanRejected, authzledger.ResolvedBySystemDeny, isolation.CodeApprovalUnavailable, "approval_refused_automatically"},
		{"user stop", isolation.PathDenialNonHumanRejected, authzledger.ResolvedByUserStop, isolation.CodeApprovalUnavailable, "approval_stopped_by_user"},
		{"host stop", isolation.PathDenialNonHumanRejected, authzledger.ResolvedByHostStop, isolation.CodeApprovalUnavailable, "approval_stopped_by_host"},
		{"expiry", isolation.PathDenialNonHumanRejected, authzledger.ResolvedByExpiry, isolation.CodeApprovalUnavailable, "approval_expired"},
		{"unclassified", isolation.PathDenialNone, "", isolation.CodeApprovalUnavailable, ""},
	} {
		reject := approvalRefusalReject(isolation.CodeLoopbackConnectDenied, tc.cause, tc.resolvedBy, "direction")
		if reject.Code != tc.code {
			t.Errorf("%s: code = %s, want %s", tc.name, reject.Code, tc.code)
		}
		if tc.fact != "" && reject.Data[tc.fact] != true {
			t.Errorf("%s: data = %v, want %s", tc.name, reject.Data, tc.fact)
		}
		if _, guided := reject.Data[toolrejection.UserGuidanceKey]; guided != (tc.code != isolation.CodeApprovalUnavailable) {
			t.Errorf("%s: user guidance carried = %t", tc.name, guided)
		}
	}
}

func TestPathRuleDenialCitesEveryMatchedRule(t *testing.T) {
	rules := []hitl.ApprovalRuleMatch{
		{Category: "command", Pattern: "cp *", Effect: "deny", Command: "cp out /x", UnitID: "rules/a"},
		{Category: "path", Pattern: "/x/**", Effect: "deny", UnitID: "rules/b"},
	}
	reject := pathPreflightReject("command", map[string]any{"command": "cp out /x"}, tools.ToolContext{Identity: tools.InvocationIdentity{Agent: "implement"}},
		"write_root", "/x", isolation.CodeWriteRootDenied, PathPreflightResult{DenialCause: isolation.PathDenialApprovalRule, DeniedRules: rules})
	if reject.Code != hitl.CodeApprovalRuleDenied || reject.Data["rule_match_count"] != "2" || reject.Data["rule_unit_id"] != "rules/a" ||
		reject.Data["command"] != "cp out /x" || reject.Data["path"] != "/x" {
		t.Fatalf("reject = %+v", reject)
	}
}

type answeringLoopbackGate struct{ result tools.LoopbackConnectResult }

func (g answeringLoopbackGate) Await(context.Context, tools.LoopbackConnectAsk) (tools.LoopbackConnectResult, error) {
	return g.result, nil
}
func (answeringLoopbackGate) SessionLoopbackGrant(context.Context, string, string) (bool, []uint16) {
	return false, nil
}

func TestLoopbackDenialCodeFollowsWhoRefused(t *testing.T) {
	args := map[string]any{"capability_request": map[string]any{"loopback_connect": map[string]any{"ports": []any{float64(8000)}}}}
	tc := tools.ToolContext{Invocation: tools.Invocation{Contract: toolcontract.Contract{Capabilities: toolcontract.CapabilityLoopbackConnect}}}
	for _, tc2 := range []struct {
		result tools.LoopbackConnectResult
		code   string
	}{
		{tools.LoopbackConnectResult{Denied: true, DenialCause: isolation.PathDenialUserDeclined}, isolation.CodeLoopbackConnectDenied},
		{tools.LoopbackConnectResult{Denied: true, DenialCause: isolation.PathDenialNonHumanRejected, ApprovalResolvedBy: authzledger.ResolvedByPolicy}, isolation.CodeApprovalUnavailable},
	} {
		executor := NewExecutor(nil, nil, "")
		executor.Capabilities.SetLoopbackConnectGate(answeringLoopbackGate{tc2.result})
		_, err := executor.Capabilities.preflightLoopbackConnectCapability(t.Context(), "command", args, tc)
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != tc2.code {
			t.Errorf("result %+v: err = %v, want %s", tc2.result, err, tc2.code)
		}
	}
}
