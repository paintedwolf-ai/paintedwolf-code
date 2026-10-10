package hitl_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func planFixture(t *testing.T, kind hitl.ApprovalSubjectKind, options []hitl.ApprovalOption) *hitl.ApprovalPlan {
	t.Helper()
	gateName := api.GateUserRule
	stage := hitl.ApprovalStagePreSpawn
	switch kind {
	case hitl.ApprovalSubjectAction, hitl.ApprovalSubjectActionSet:
		gateName = api.GateExplicitApprovalRequest
	case hitl.ApprovalSubjectSocketSet, hitl.ApprovalSubjectDirectIP:
		gateName = api.GateUnobservedChannel
	case hitl.ApprovalSubjectSecret:
		gateName = api.GateSecretOutbound
		stage = hitl.ApprovalStagePreSend
	case hitl.ApprovalSubjectWriteRootSet:
		gateName = api.GateSensitiveLocation
	case hitl.ApprovalSubjectPackageSet:
		gateName = api.GateRemotePackageExecution
	case hitl.ApprovalSubjectDestinationSet:
		gateName = api.GateFirstHost
		stage = hitl.ApprovalStagePreDial
	case hitl.ApprovalSubjectProcessControl, hitl.ApprovalSubjectHostExecution, hitl.ApprovalSubjectLocalListen,
		hitl.ApprovalSubjectLoopbackConnect, hitl.ApprovalSubjectReadPathSet:
	}
	target := hitl.ApprovalTarget{Kind: "t", Label: "x"}
	if kind == hitl.ApprovalSubjectSecret {
		target.Details = map[string]any{"generic_shape": "a1b2c3a1b2c3a1b2c3a1 (20 characters)"}
	}
	plan, err := hitl.NewApprovalPlan(
		hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
Presentation: hitl.ActionPresentation{
Command: "pwd",
},
Scope: hitl.ActionScope{
SessionID: "s1",
},
},
		stage,
		hitl.ApprovalSubject{Kind: kind, Title: "Fixture", Targets: []hitl.ApprovalTarget{target}},
		hitl.ApprovalPresentation{
			Action: "Act", Impact: "Impact", Gate: gateName,
			Cited: []hitl.PresentedFact{{Gate: gateName, Key: "k", Value: "v", Source: "test"}},
		},
		[]api.ApprovalGate{gateName},
		options,
		hitl.FaceContext{})
	testutil.FailErr(t, "NewApprovalPlan", err)
	return plan
}

func TestPlanFaceIsComputedNotSupplied(t *testing.T) {
	once := hitl.CurrentActionOption()
	task := hitl.ApprovalOption{
		ID: "task", Kind: hitl.ApprovalOptionLease, Rung: hitl.ApprovalRungChat, Scope: hitl.ApprovalGrantScopeChat,
		Title: hitl.TitleAllowForThisChat, Coverage: "c", ExpiresWhen: hitl.ExpiresWhenChatDeleted, ReaskWhen: "r",
		DecisionAction: hitl.ApprovalOptionApprove,
		Authority:      []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthorityGenericGrant, Grant: &hitl.ApprovalGrant{ID: "g1"}}},
	}
	day := hitl.ApprovalOption{
		ID: "day", Kind: hitl.ApprovalOptionLease, Rung: hitl.ApprovalRungDay, Scope: hitl.ApprovalGrantScopeProject,
		Title: hitl.TitleAllowFor1Day, Coverage: "c", ExpiresWhen: hitl.ExpiresIn1DayOrRevoked, ReaskWhen: "r",
		DecisionAction: hitl.ApprovalOptionApprove,
		Authority: []hitl.ApprovalAuthorityDelta{{
			Kind: hitl.AuthorityGenericGrant,
			Grant: &hitl.ApprovalGrant{
				ID: "g2", Scope: hitl.ApprovalGrantScopeProject, ProjectID: "proj-1",
			},
			TTLSeconds: hitl.DayRungTTLSeconds,
		}},
	}
	ordinary := []hitl.ApprovalOption{day, once, task}
	for _, kind := range []hitl.ApprovalSubjectKind{
		hitl.ApprovalSubjectAction,
		hitl.ApprovalSubjectActionSet,
		hitl.ApprovalSubjectDestinationSet,
		hitl.ApprovalSubjectPackageSet,
	} {
		plan := planFixture(t, kind, ordinary)
		wantFace := "task"
		if plan.RecommendedOptionID != wantFace {
			t.Fatalf("%s face = %q, want %s", kind, plan.RecommendedOptionID, wantFace)
		}
		ids := map[string]int{}
		for _, option := range plan.Options {
			ids[option.ID]++
		}
		if ids[plan.RecommendedOptionID] != 1 {
			t.Fatalf("%s face id %q not unique among options", kind, plan.RecommendedOptionID)
		}
	}
	if plan := planFixture(t, hitl.ApprovalSubjectAction, ordinary); plan.Options[0].Rung != hitl.ApprovalRungOnce {
		t.Fatalf("order[0] = %s, want once", plan.Options[0].Rung)
	}

	onceOnly := planFixture(t, hitl.ApprovalSubjectAction, []hitl.ApprovalOption{once})
	if onceOnly.RecommendedOptionID != once.ID {
		t.Fatalf("once-only face = %q, want %q", onceOnly.RecommendedOptionID, once.ID)
	}

	redacted := hitl.SendRedactedOption()
	unchanged := hitl.SendUnchangedOption()
	secretDay := day
	secretDay.ID = "secret-day"
	secret := planFixture(t, hitl.ApprovalSubjectSecret, []hitl.ApprovalOption{secretDay, unchanged, redacted})
	// Redaction stays on the card but is never its face: on the seams that
	// carry credentials it fails the call rather than softening it.
	if secret.RecommendedOptionID != "send_unchanged" {
		t.Fatalf("secret face = %q, want send_unchanged", secret.RecommendedOptionID)
	}
	if secret.RecommendedOptionID == "secret-day" {
		t.Fatal("secret must never face the day fingerprint lease")
	}
}

// A seam that cannot be rewritten keeps redaction in place, disabled — and
// faces the send instead; the recommended option is always one that can be pressed.
func TestUnrewritableSecretFacesTheSendNotDisabledRedaction(t *testing.T) {
	gateName := api.GateSecretOutbound
	plan, err := hitl.NewApprovalPlan(
		hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
Presentation: hitl.ActionPresentation{
Command: "curl",
},
Scope: hitl.ActionScope{
SessionID: "s1",
},
},
		hitl.ApprovalStagePreSend,
		hitl.ApprovalSubject{
			Kind: hitl.ApprovalSubjectSecret, Title: "Fixture",
			Targets: []hitl.ApprovalTarget{{
				Kind: "secret", Label: "HTTP Basic Authorization Header",
				Details: map[string]any{"generic_shape": "a1b2c3a1b2c3a1b2c3a1 (20 characters)"},
			}},
		},
		hitl.ApprovalPresentation{
			Action: "Send command", Impact: "Impact", Gate: gateName,
			Cited:      []hitl.PresentedFact{{Gate: gateName, Key: "k", Value: "v", Source: "test"}},
			OptionNote: "Redaction is not offered here: replacing the value would run a command neither you nor the model wrote.",
		},
		[]api.ApprovalGate{gateName},
		[]hitl.ApprovalOption{hitl.UnavailableSendRedactedOption(""), hitl.SendUnchangedOption()},
		hitl.FaceContext{})
	testutil.FailErr(t, "NewApprovalPlan", err)
	if plan.RecommendedOptionID != "send_unchanged" {
		t.Fatalf("face = %q, want send_unchanged", plan.RecommendedOptionID)
	}
	redactedRow, ok := plan.Option("send_redacted")
	if !ok || !redactedRow.Disabled {
		t.Fatalf("send_redacted = %+v, want present and disabled", redactedRow)
	}
	if _, ok := plan.Option("send_unchanged"); !ok {
		t.Fatal("send_unchanged missing")
	}
}

func TestCapabilityWideningActionSetFacesTask(t *testing.T) {
	once := hitl.CurrentActionOption()
	task := hitl.ApprovalOption{
		ID: "task", Kind: hitl.ApprovalOptionLease, Rung: hitl.ApprovalRungChat, Scope: hitl.ApprovalGrantScopeChat,
		Title: hitl.TitleAllowForThisChat, Coverage: "c", ExpiresWhen: hitl.ExpiresWhenChatDeleted, ReaskWhen: "r",
		DecisionAction: hitl.ApprovalOptionApprove,
		Authority: []hitl.ApprovalAuthorityDelta{
			{Kind: hitl.AuthorityLocalListenChat, Grant: &hitl.ApprovalGrant{ID: "listen"}, ChatSessionID: "s1"},
			{Kind: hitl.AuthorityLoopbackConnectChat, Grant: &hitl.ApprovalGrant{ID: "connect"}, ChatSessionID: "s1"},
		},
	}
	day := hitl.ApprovalOption{
		ID: "day", Kind: hitl.ApprovalOptionLease, Rung: hitl.ApprovalRungDay, Scope: hitl.ApprovalGrantScopeChat,
		Title: hitl.TitleAllowFor1Day, Coverage: "c", ExpiresWhen: hitl.ExpiresIn1DayOrRevoked, ReaskWhen: "r",
		DecisionAction: hitl.ApprovalOptionApprove,
		Authority: []hitl.ApprovalAuthorityDelta{
			{Kind: hitl.AuthorityLocalListenChat, Grant: &hitl.ApprovalGrant{ID: "listen-day"}, ChatSessionID: "s1", TTLSeconds: hitl.DayRungTTLSeconds},
			{Kind: hitl.AuthorityLoopbackConnectChat, Grant: &hitl.ApprovalGrant{ID: "connect-day"}, ChatSessionID: "s1", TTLSeconds: hitl.DayRungTTLSeconds},
		},
	}
	plan, err := hitl.NewApprovalPlan(
		hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
Presentation: hitl.ActionPresentation{
Command: "node server.js",
},
Scope: hitl.ActionScope{
SessionID: "s1",
},
},
		hitl.ApprovalStagePreSpawn,
		hitl.ApprovalSubject{
			Kind: hitl.ApprovalSubjectActionSet, Title: "Allow local network use",
			Targets: []hitl.ApprovalTarget{
				{Kind: "local_listen", Label: "local port 3000"},
				{Kind: "loopback_connect", Label: "local destination port 3000"},
			},
		},
		hitl.ApprovalPresentation{
			Action: "Local server and local connections", Impact: "Impact",
			Gate:  api.GateCapabilityWidening,
			Cited: []hitl.PresentedFact{{Gate: api.GateCapabilityWidening, Key: "capability.axis", Value: "local_listen", Source: "test"}},
		},
		[]api.ApprovalGate{api.GateCapabilityWidening},
		[]hitl.ApprovalOption{day, once, task},
		hitl.FaceContext{})
	testutil.FailErr(t, "NewApprovalPlan", err)
	if plan.RecommendedOptionID != "task" {
		t.Fatalf("capability-widening action-set face = %q, want task", plan.RecommendedOptionID)
	}
}

func TestSecretPlanRequiresGenericShape(t *testing.T) {
	_, err := hitl.NewApprovalPlan(
		hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "model_request",
},
Scope: hitl.ActionScope{
SessionID: "s1",
},
},
		hitl.ApprovalStagePreSend,
		hitl.ApprovalSubject{
			Kind: hitl.ApprovalSubjectSecret, Title: "Fixture",
			Targets: []hitl.ApprovalTarget{{Kind: "secret", Label: "Credential"}},
		},
		hitl.ApprovalPresentation{
			Action: "Send request", Impact: "Impact", Gate: api.GateSecretOutbound,
			Cited:      []hitl.PresentedFact{{Gate: api.GateSecretOutbound, Key: "k", Value: "v", Source: "test"}},
			OptionNote: "Redaction is unavailable.",
		},
		[]api.ApprovalGate{api.GateSecretOutbound},
		[]hitl.ApprovalOption{hitl.UnavailableSendRedactedOption(""), hitl.SendUnchangedOption()},
		hitl.FaceContext{})
	if err == nil || !strings.Contains(err.Error(), "generic shape") {
		t.Fatalf("error = %v, want missing generic shape", err)
	}
}

func TestPlanRungsAreUniqueAndOrdered(t *testing.T) {
	dup := hitl.CurrentActionOption()
	dup2 := hitl.CurrentActionOption()
	dup2.ID = "other-once"
	_, err := hitl.NewApprovalPlan(
		hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
Presentation: hitl.ActionPresentation{
Command: "pwd",
},
},
		hitl.ApprovalStagePreSpawn,
		hitl.ApprovalSubject{Kind: hitl.ApprovalSubjectAction, Title: "T", Targets: []hitl.ApprovalTarget{{Kind: "t", Label: "x"}}},
		hitl.ApprovalPresentation{
			Action: "A", Impact: "I", Gate: "explicit_approval_request",
			Cited: []hitl.PresentedFact{{Gate: "explicit_approval_request", Key: "k", Value: "v", Source: "t"}},
		},
		[]api.ApprovalGate{api.GateExplicitApprovalRequest},
		[]hitl.ApprovalOption{dup, dup2},
		hitl.FaceContext{})
	if err == nil {
		t.Fatal("duplicate (group, rung) must fail")
	}
}

func TestGateProvenanceIsAlwaysRequired(t *testing.T) {
	_, err := hitl.NewApprovalPlan(
		hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
Presentation: hitl.ActionPresentation{
Command: "pwd",
},
},
		hitl.ApprovalStagePreSpawn,
		hitl.ApprovalSubject{Kind: hitl.ApprovalSubjectAction, Title: "T", Targets: []hitl.ApprovalTarget{{Kind: "t", Label: "x"}}},
		hitl.ApprovalPresentation{Action: "A", Impact: "I"},
		nil,
		[]hitl.ApprovalOption{hitl.CurrentActionOption()},
		hitl.FaceContext{})
	if err == nil {
		t.Fatal("missing gate provenance must fail")
	}
}

func TestDirectIPPlanAllowedAfterFailure(t *testing.T) {
	lease := hitl.DirectIPLease{
		ActionDigest: "action", RequestDigest: "req", ConfinementDigest: "conf",
		DeclaredDestinations: []string{"docker-01:22"}, CommandSummary: "ssh host true",
	}
	grant := hitl.ApprovalGrant{
		ID: "grant_task", Scope: hitl.ApprovalGrantScopeChat,
		Predicate: hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryDirectIP, Pattern: lease.IdentityKey()},
		Title:     hitl.TitleAllowForThisChat, Coverage: "this exact command",
		ExpiresWhen: hitl.ExpiresWhenChatDeleted, ReaskWhen: "the command changes",
	}
	option := hitl.GrantOption(hitl.ApprovalGrantOffer{
		ID: grant.ID, Rung: hitl.ApprovalRungChat, Scope: hitl.ApprovalGrantScopeChat,
		Title: grant.Title, Coverage: grant.Coverage, ExpiresWhen: grant.ExpiresWhen,
		ReaskWhen: grant.ReaskWhen, Grant: grant,
		Authority: []hitl.ApprovalAuthorityDelta{{
			Kind: hitl.AuthorityDirectIPChat, Grant: &grant, ChatSessionID: "s1", DirectIPLease: &lease,
		}},
	})
	_, err := hitl.NewApprovalPlan(
		hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
Presentation: hitl.ActionPresentation{
Command: "ssh host true",
},
Scope: hitl.ActionScope{
SessionID: "s1",
},
},
		hitl.ApprovalStagePreSpawn,
		hitl.ApprovalSubject{Kind: hitl.ApprovalSubjectDirectIP, Title: "Allow direct network access",
			Targets: []hitl.ApprovalTarget{{Kind: "direct_ip", Label: "this exact command, unobserved"}}},
		hitl.ApprovalPresentation{
			Action: "Use direct network access", Impact: hitl.DirectIPWhat, Gate: api.GateUnobservedChannel,
			Cited: []hitl.PresentedFact{{Gate: api.GateUnobservedChannel, Key: "k", Value: "v", Source: "test"}},
		},
		[]api.ApprovalGate{api.GateUnobservedChannel},
		[]hitl.ApprovalOption{option},
		hitl.FaceContext{})
	testutil.FailErr(t, "pre-spawn direct-IP plan", err)
}
