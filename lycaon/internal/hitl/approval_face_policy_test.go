package hitl_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func facePolicyOptions(disableProject bool) []hitl.ApprovalOption {
	task := hitl.ApprovalGrant{ID: "grant_chat", Scope: hitl.ApprovalGrantScopeChat, Title: hitl.TitleAllowForThisChat}
	project := hitl.ApprovalGrant{ID: "grant_project", Scope: hitl.ApprovalGrantScopeProject, ProjectID: "proj", Title: hitl.TitleAllowForThisProject}
	options := []hitl.ApprovalOption{
		hitl.CurrentActionOption(),
		{
			ID: "chat", Kind: hitl.ApprovalOptionLease, Rung: hitl.ApprovalRungChat, Scope: hitl.ApprovalGrantScopeChat,
			Title: hitl.TitleAllowForThisChat, Coverage: "c", ExpiresWhen: hitl.ExpiresWhenChatDeleted, ReaskWhen: "r",
			DecisionAction: hitl.ApprovalOptionApprove,
			Authority:      []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthorityGenericGrant, Grant: &task}},
		},
		{
			ID: "project", Kind: hitl.ApprovalOptionLease, Rung: hitl.ApprovalRungProject, Scope: hitl.ApprovalGrantScopeProject,
			Title: hitl.TitleAllowForThisProject, Coverage: "c", ExpiresWhen: hitl.ExpiresIn7DaysOrRevoked, ReaskWhen: "r",
			DecisionAction: hitl.ApprovalOptionApprove, Disabled: disableProject,
			Authority: []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthorityGenericGrant, Grant: &project}},
		},
	}
	if disableProject {
		options[2].Note = hitl.NoteEndsWithChat
	}
	return options
}

func facePolicyPlan(t *testing.T, face hitl.FaceContext, options []hitl.ApprovalOption) *hitl.ApprovalPlan {
	t.Helper()
	return facePolicyPlanFor(t, hitl.ApprovalSubjectAction, face, options)
}

func facePolicyPlanFor(t *testing.T, kind hitl.ApprovalSubjectKind, face hitl.FaceContext, options []hitl.ApprovalOption) *hitl.ApprovalPlan {
	t.Helper()
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
Scope: hitl.ActionScope{
SessionID: "chat-1",
RootSessionID: "chat-1",
},
Presentation: hitl.ActionPresentation{
Command: "curl https://example.com",
},
}
	plan, err := hitl.NewApprovalPlan(
		action, hitl.ApprovalStagePreSpawn,
		hitl.ApprovalSubject{Kind: kind, Title: "Approve command",
			Targets: []hitl.ApprovalTarget{{Kind: "action", Label: action.Presentation.Command}}},
		hitl.ApprovalPresentation{Action: "Run command", Impact: "Reach the network.", Gate: api.GateUserRule,
			Cited: []hitl.PresentedFact{{Gate: api.GateUserRule, Key: "k", Value: "v", Source: "t"}}},
		[]api.ApprovalGate{api.GateUserRule}, options, face)
	testutil.FailErr(t, "NewApprovalPlan", err)
	return plan
}

// Durable subjects that offer a project lease still face the chat rung.
func TestEveryCardFacesTheChatRung(t *testing.T) {
	t.Parallel()
	for _, kind := range []hitl.ApprovalSubjectKind{hitl.ApprovalSubjectAction, hitl.ApprovalSubjectPackageSet} {
		plan := facePolicyPlanFor(t, kind, hitl.FaceContext{}, facePolicyOptions(false))
		if plan.RecommendedOptionID != "chat" {
			t.Errorf("%s faces %q, want the chat rung", kind, plan.RecommendedOptionID)
		}
	}
}

// A disabled rung is never the face.
func TestDisabledRungIsNeverTheFace(t *testing.T) {
	t.Parallel()
	withDisabled := facePolicyPlan(t, hitl.FaceContext{}, facePolicyOptions(true))
	if face, _ := withDisabled.Option(withDisabled.RecommendedOptionID); face.Disabled {
		t.Fatalf("a disabled rung was faced: %+v", face)
	}
}

// Without a tracking option, Balanced presents the chat release immediately.
func TestSecretFaceUsesTaskReleaseFromFirstReview(t *testing.T) {
	t.Parallel()
	chatRelease := hitl.ApprovalGrant{ID: "grant_release_chat", Scope: hitl.ApprovalGrantScopeChat, Title: hitl.TitleSendUnchangedForThisChat}
	options := []hitl.ApprovalOption{
		hitl.SendRedactedOption(),
		hitl.SendUnchangedOption(),
		{
			ID: "release_chat", Kind: hitl.ApprovalOptionLease, Rung: hitl.ApprovalRungChat, Scope: hitl.ApprovalGrantScopeChat,
			Title: hitl.TitleSendUnchangedForThisChat, Coverage: "c", ExpiresWhen: hitl.ExpiresWhenChatDeleted, ReaskWhen: "r",
			DecisionAction: hitl.ApprovalOptionApprove,
			Authority:      []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthorityGenericGrant, Grant: &chatRelease}},
		},
	}
	build := func(face hitl.FaceContext) *hitl.ApprovalPlan {
		action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "model_request",
},
Scope: hitl.ActionScope{
SessionID: "chat-1",
RootSessionID: "chat-1",
},
}
		plan, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSend,
			hitl.ApprovalSubject{Kind: hitl.ApprovalSubjectSecret, Title: "Credential detected",
				Targets: []hitl.ApprovalTarget{{Kind: "secret", Label: "AWS access key ID", Details: map[string]any{"generic_shape": "a1 (2 characters)"}}}},
			hitl.ApprovalPresentation{Action: "Send model request", Impact: "Sends a credential.", Gate: api.GateSecretOutbound,
				Cited: []hitl.PresentedFact{{Gate: api.GateSecretOutbound, Key: "secret.rule", Value: "AWS access key ID", Source: "payload_lens"}}},
			[]api.ApprovalGate{api.GateSecretOutbound}, options, face)
		testutil.FailErr(t, "NewApprovalPlan secret", err)
		return plan
	}
	if got := build(hitl.FaceContext{}).RecommendedOptionID; got != "release_chat" {
		t.Fatalf("secret ask faces %q, want the chat release", got)
	}
}
