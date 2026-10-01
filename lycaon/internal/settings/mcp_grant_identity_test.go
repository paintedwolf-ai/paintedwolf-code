package settings

import (
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func registeredMCPAction() hitl.ProposedAction {
	return hitl.ProposedAction{
		Tool:             "mcp_protected_validation_credential_present",
		ApprovalCategory: "mcp", ApprovalSubject: "Protected validation.credential_present",
		SessionID: "chat-a", ProjectID: "project-a",
		Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{"/tmp/project-a"}},
	}
}

func TestRegisteredMCPGrantScopeBoundaries(t *testing.T) {
	for _, scope := range []hitl.ApprovalGrantScope{hitl.ApprovalGrantScopeChat, hitl.ApprovalGrantScopeProject} {
		t.Run(string(scope), func(t *testing.T) {
			approvals := ladderGate(t)
			testutil.FailErr(t, "set strict posture", approvals.store.PutGlobal(ApprovalConfig{Posture: gate.PostureStrict}))
			action := registeredMCPAction()
			first, err := approvals.Evaluate(t.Context(), action)
			testutil.FailErr(t, "evaluate unapproved MCP action", err)
			if !first.Required() {
				t.Fatalf("unapproved MCP action must ask: %+v", first)
			}
			var selected *hitl.ApprovalGrantOffer
			for _, offer := range approvals.GrantOffers(action, first) {
				if offer.Scope == scope && offer.Grant.Predicate.Category == "mcp" {
					copy := offer
					selected = &copy
					break
				}
			}
			if selected == nil {
				t.Fatalf("missing %s MCP predicate offer", scope)
			}
			selected.Grant.GrantedByPersonID = testutil.HostOwner().ID
			_, err = approvals.ApplyGrant(selected.Grant)
			testutil.FailErr(t, "apply MCP grant", err)
			assertMCPGrantBoundaries(t, approvals, action, scope)
			assertMCPDriftRequiresConsent(t, approvals, action)
			assertMCPDenyOverridesGrant(t, approvals, action)
		})
	}
}

type changedMCPPin string

func (pin changedMCPPin) ToolDefinitionChanged(tool string) bool { return string(pin) == tool }

func assertMCPDriftRequiresConsent(t *testing.T, approvals *RuleApprovalGate, action hitl.ProposedAction) {
	t.Helper()
	approvals.sources.Pins = changedMCPPin(action.Tool)
	result, err := approvals.Evaluate(t.Context(), action)
	testutil.FailErr(t, "evaluate leased MCP definition drift", err)
	if !result.Required() || result.Decision.Primary != api.GateConsentDrift {
		t.Fatalf("definition drift must require renewed consent: %+v", result)
	}
	approvals.sources.Pins = inertPins{}
}

func assertMCPDenyOverridesGrant(t *testing.T, approvals *RuleApprovalGate, action hitl.ProposedAction) {
	t.Helper()
	config := approvals.effectiveConfig(action)
	config.Rules = []ApprovalRule{{Category: ApprovalCategoryMCP, Pattern: action.ApprovalSubject, Effect: ApprovalEffectDeny}}
	testutil.FailErr(t, "install canonical MCP deny rule", approvals.store.PutGlobal(config))
	result, err := approvals.Evaluate(t.Context(), action)
	testutil.FailErr(t, "evaluate denied leased MCP action", err)
	if !result.Denied || result.AutoApproved() {
		t.Fatalf("canonical deny must override existing grant: %+v", result)
	}
}

func assertMCPGrantBoundaries(t *testing.T, approvals *RuleApprovalGate, action hitl.ProposedAction, scope hitl.ApprovalGrantScope) {
	t.Helper()
	cases := []struct {
		name    string
		change  func(*hitl.ProposedAction)
		covered bool
	}{
		{"same action", func(*hitl.ProposedAction) {}, true},
		{"other chat", func(a *hitl.ProposedAction) { a.SessionID = "chat-b" }, scope == hitl.ApprovalGrantScopeProject},
		{"child task", func(a *hitl.ProposedAction) {
			a.SessionID, a.RootSessionID = "worker", action.SessionID
		}, true},
		{"explicit task target", func(a *hitl.ProposedAction) {
			a.SessionID, a.RootSessionID = "worker", action.SessionID
		}, true},
		{"other project", func(a *hitl.ProposedAction) { a.ProjectID = "project-b" }, false},
		{"other roots", func(a *hitl.ProposedAction) { a.Contained.Roots = []string{"/tmp/project-b"} }, false},
		{"other tool", func(a *hitl.ProposedAction) { a.ApprovalSubject = "Protected validation.write" }, false},
		{"colliding public name", func(a *hitl.ProposedAction) { a.ApprovalSubject = "Protected-validation.credential_present" }, false},
		{"argument identity spoof", func(a *hitl.ProposedAction) {
			a.ApprovalSubject = "Other provider.credential_present"
			a.Args = map[string]any{"approval_subject": action.ApprovalSubject}
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := action
			tc.change(&candidate)
			result, err := approvals.Evaluate(t.Context(), candidate)
			testutil.FailErr(t, "evaluate MCP grant boundary", err)
			if result.AutoApproved() != tc.covered || result.Required() == tc.covered {
				t.Fatalf("covered=%t, result=%+v", tc.covered, result)
			}
		})
	}
}

func TestMCPRulesUseCanonicalIdentity(t *testing.T) {
	action := registeredMCPAction()
	for _, pattern := range []string{action.ApprovalSubject, "Protected validation.*", "*"} {
		if !ruleMatches(ApprovalRule{Category: ApprovalCategoryMCP, Pattern: pattern}, action) {
			t.Fatalf("canonical MCP rule %q did not match", pattern)
		}
	}
	if ruleMatches(ApprovalRule{Category: ApprovalCategoryMCP, Pattern: action.Tool}, action) {
		t.Fatal("public alias replaced a supplied canonical identity")
	}
	action.Tool = "command"
	if ruleMatches(ApprovalRule{Category: ApprovalCategoryMCP, Pattern: "*"}, action) {
		t.Fatal("MCP predicate covered a native command")
	}
}

func TestMCPGrantIdentityTreatsPatternSyntaxLiterally(t *testing.T) {
	for _, subject := range []string{
		"provider*.read", "provider?.read", "provider[ab].read", `provider\name.read`,
	} {
		t.Run(subject, func(t *testing.T) {
			action := registeredMCPAction()
			action.ApprovalSubject = subject
			predicate := GrantPredicateForAction(action)
			grant := hitl.ApprovalGrant{
				ProjectID: action.ProjectID,
				Predicate: hitl.ApprovalGrantPredicate{Category: string(predicate.Category), Pattern: predicate.Pattern},
			}
			if !grantMatchesAction(grant, action) {
				t.Fatal("literal MCP identity did not cover itself")
			}
			for _, other := range []string{"providerA.read", "providera.read", "providerXYZ.read", "providername.read"} {
				action.ApprovalSubject = other
				if grantMatchesAction(grant, action) {
					t.Fatalf("grant for %q covered distinct identity %q", subject, other)
				}
			}
		})
	}
}

func TestMCPAuthorityRequiresRegisteredIdentity(t *testing.T) {
	for _, tool := range []string{"mcp_docs_search", "mcp.docs.search", "command"} {
		for _, category := range []string{"", "mcp", "tool"} {
			for _, subject := range []string{"", " ", "docs.search"} {
				t.Run(tool+"/"+category+"/"+subject, func(t *testing.T) {
					action := registeredMCPAction()
					action.Tool, action.ApprovalCategory, action.ApprovalSubject = tool, category, subject
					want := tool == "mcp_docs_search" && category == "mcp" && subject == "docs.search"
					if got := ruleMatches(ApprovalRule{Category: ApprovalCategoryMCP, Pattern: "*"}, action); got != want {
						t.Fatalf("MCP rule match=%t, want %t", got, want)
					}
					for _, pattern := range []string{"", " ", "docs.search", tool} {
						grant := hitl.ApprovalGrant{
							ProjectID: action.ProjectID,
							Predicate: hitl.ApprovalGrantPredicate{Category: "mcp", Pattern: pattern},
						}
						if got := grantMatchesAction(grant, action); got != (want && pattern == subject) {
							t.Fatalf("MCP grant %q match=%t, registered=%t", pattern, got, want)
						}
					}
				})
			}
		}
	}
}
