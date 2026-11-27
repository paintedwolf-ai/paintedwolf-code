package toolexecution

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/capabilitygrants"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/toolapproval"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

type identityCheckpointManager struct {
	hitl.CheckpointManager
}

type identityCoalescer struct {
	toolapproval.ToolApprovalCoalesce
	called bool
}

func (c *identityCoalescer) Begin(string, string) (approvalstate.ToolApprovalCoalesceBegin, string) {
	c.called = true
	return approvalstate.ToolApprovalCoalesceMint, ""
}

func TestApprovalRejectsUnencodableIdentityBeforeCoalescing(t *testing.T) {
	for _, coalesceKey := range []string{"", "explicit-key"} {
		t.Run("key="+coalesceKey, func(t *testing.T) {
			coalescer := &identityCoalescer{}
			executor := func() *Executor {
				e := NewExecutor(nil, nil, "")
				e.Approvals.checkpointMgr = identityCheckpointManager{}
				e.Approvals.approvalCoalesce = coalescer
				return e
			}()
			action := hitl.ProposedAction{
				Invocation: hitl.ActionInvocation{
					Tool: "command",
					Args: map[string]any{"invalid": make(chan int)},
				},
			}
			response, err := executor.Approvals.raiseAndWaitToolApproval(t.Context(), toolApprovalRaise{Action: action, CoalesceKey: coalesceKey})
			if err == nil || response != nil || coalescer.called {
				t.Fatalf("unencodable action reached coalescing: response=%+v err=%v coalesced=%t", response, err, coalescer.called)
			}
			permission := &hitl.SecretPermission{}
			if key := permission.Key(hitl.GrantKey(action)); key != "" {
				t.Fatalf("secret suffix made an unusable identity usable: %q", key)
			}
			if key := directIPCoalesceKey(hitl.GrantKey(action), "request"); key != "" {
				t.Fatalf("direct-IP suffix made an unusable identity usable: %q", key)
			}
			if key := capabilitygrants.SocketSetCoalesceKey(action, []confine.SocketGrant{{ApprovedPath: "/socket", ResolvedPath: "/socket"}}); key != "" {
				t.Fatalf("socket suffix made an unusable identity usable: %q", key)
			}
		})
	}
}

func (identityCheckpointManager) ApprovalReviewSuppressed(context.Context, string, string) (bool, error) {
	return false, nil
}

// A widening posture adds the command's network right after the host's chat
// permission and keeps every other rung where it was, so the face, the first
// primary chat rung, stays the host.
func TestCommandNetworkSitsBesideTheHostsChatPermission(t *testing.T) {
	once := hitl.ApprovalGrantOffer{ID: "once", Rung: hitl.ApprovalRungOnce}
	host := hitl.ApprovalGrantOffer{ID: "host", Rung: hitl.ApprovalRungChat}
	project := hitl.ApprovalGrantOffer{ID: "project", Rung: hitl.ApprovalRungProject}
	menu := hitl.ApprovalGrantOffer{ID: "menu", Rung: hitl.ApprovalRungChat, Group: hitl.GroupAlsoAllow}
	command := hitl.ApprovalGrantOffer{ID: "command", Rung: hitl.ApprovalRungChat}

	ids := func(offers []hitl.ApprovalGrantOffer) []string {
		out := make([]string, 0, len(offers))
		for _, offer := range offers {
			out = append(out, offer.ID)
		}
		return out
	}
	if got := ids(besideChatSlot([]hitl.ApprovalGrantOffer{once, menu, host, project}, command)); !slices.Equal(got, []string{"once", "menu", "host", "command", "project"}) {
		t.Fatalf("ladder = %v", got)
	}
	if got := ids(besideChatSlot([]hitl.ApprovalGrantOffer{once, project}, command)); !slices.Equal(got, []string{"once", "project", "command"}) {
		t.Fatalf("ladder without a chat slot = %v", got)
	}
}

func repeatRaise(reasonKey, subject string) toolApprovalRaise {
	return toolApprovalRaise{
		Decision: &gate.Decision{ReasonKey: reasonKey},
		Plan:     &hitl.ApprovalPlan{Subject: hitl.ApprovalSubject{Targets: []hitl.ApprovalTarget{{Label: subject}}}},
	}
}

// Asking again about the same subject escalates as a repeat, as asking about a new subject does.
func TestRepeatedAsksEscalateForTheSameAndDistinctSubjects(t *testing.T) {
	approvals := &Approvals{}
	approvals.SetGateRepeatLedger(approvalstate.NewGateRepeatLedger())

	if repeat := approvals.noteRepeatAsk("chat", repeatRaise("outside_root", "notes.txt")); repeat != nil {
		t.Fatalf("first ask = %+v, want no repeat context", repeat)
	}
	repeat := approvals.noteRepeatAsk("chat", repeatRaise("outside_root", "notes.txt"))
	if repeat == nil || repeat.Asks != 2 || repeat.Count != 1 {
		t.Fatalf("second ask on one subject = %+v, want asks 2 over 1 subject", repeat)
	}
	repeat = approvals.noteRepeatAsk("chat", repeatRaise("outside_root", "plan.txt"))
	if repeat == nil || repeat.Asks != 3 || repeat.Count != 2 || len(repeat.Subjects) != 1 || repeat.Subjects[0] != "notes.txt" {
		t.Fatalf("ask on a new subject = %+v, want asks 3 over 2 subjects naming the earlier one", repeat)
	}
	if repeat := approvals.noteRepeatAsk("other-chat", repeatRaise("outside_root", "notes.txt")); repeat != nil {
		t.Fatalf("first ask in another chat = %+v, want no repeat context", repeat)
	}
}

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
