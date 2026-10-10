package settings_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// agentPolicyGate evaluates at one posture for a project with a skill and
// nested instructions.
func agentPolicyGate(t *testing.T, posture gate.Posture) (hitl.ApprovalGate, string) {
	t.Helper()
	stageBundledApprovalsYAML(t, "approval_posture: "+string(posture)+"\nrules:\n")
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	testutil.FailErr(t, "approval store", err)
	project := t.TempDir()
	testutil.FailErr(t, "create overlay", os.MkdirAll(filepath.Join(project, settingsoverlay.DirName()), 0o700))
	return settings.NewRuleApprovalGate(store, settings.NoSources()), project
}

func agentPolicyWrite(t *testing.T, project, rel string) hitl.ProposedAction {
	t.Helper()
	path := filepath.Join(project, filepath.FromSlash(rel))
	target, ok := hitl.AgentPolicyTargetFor(path, project)
	if !ok {
		t.Fatalf("%s is not agent policy", rel)
	}
	return hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "write",
Files: []string{path},
},
Scope: hitl.ActionScope{
ProjectID: "project",
ProjectDir: project,
SessionID: "task",
},
Mutations: hitl.ActionMutations{
AgentPolicy: []hitl.AgentPolicyTarget{target},
FileChanges: []api.ApprovalFileChange{{Path: path, Operation: "write", After: "changed"}},
},
}
}

func asksAgentPolicy(result *hitl.ApprovalResult) bool {
	return result != nil && result.Required() && slices.Contains(result.Decision.Gates(), api.GateAgentPolicyChange)
}

func TestAgentPolicyFollowsThePosture(t *testing.T) {
	for _, tc := range []struct {
		posture gate.Posture
		asks    bool
	}{
		{gate.PostureLight, false},
		{gate.PostureBalanced, true},
		{gate.PostureStrict, true},
	} {
		t.Run(string(tc.posture), func(t *testing.T) {
			approvalGate, project := agentPolicyGate(t, tc.posture)
			result, err := approvalGate.Evaluate(t.Context(), agentPolicyWrite(t, project, "AGENTS.md"))
			testutil.FailErr(t, "evaluate", err)
			if asksAgentPolicy(result) != tc.asks {
				t.Fatalf("%s asks=%v, want %v: %+v", tc.posture, asksAgentPolicy(result), tc.asks, result)
			}
			if result.Denied {
				t.Fatalf("agent policy was refused: %+v", result)
			}
		})
	}
}

// Approvals switched off ask nothing; confinement still holds the floor.
func TestAgentPolicyIsSilentWithApprovalsOff(t *testing.T) {
	approvalGate, project := neverAskGate(t, true)
	testutil.FailErr(t, "create overlay", os.MkdirAll(filepath.Join(project, settingsoverlay.DirName()), 0o700))
	for _, off := range []hitl.ApprovalGate{approvalGate, settings.NewBypassApprovalGate()} {
		result, err := off.Evaluate(t.Context(), agentPolicyWrite(t, project, "AGENTS.md"))
		testutil.FailErr(t, "evaluate", err)
		if result.Required() || result.Denied {
			t.Fatalf("approvals off still stopped agent policy: %+v", result)
		}
	}
}

// The card faces a chat lease at every posture and never offers quiet.
func TestAgentPolicyOffersATaskLeaseAndNoQuiet(t *testing.T) {
	for _, posture := range []gate.Posture{gate.PostureBalanced, gate.PostureStrict} {
		t.Run(string(posture), func(t *testing.T) {
			approvalGate, project := agentPolicyGate(t, posture)
			action := agentPolicyWrite(t, project, "AGENTS.md")
			result, err := approvalGate.Evaluate(t.Context(), action)
			testutil.FailErr(t, "evaluate", err)
			offers := approvalGate.GrantOffers(action, result)
			task := slices.IndexFunc(offers, func(o hitl.ApprovalGrantOffer) bool {
				return o.Rung == hitl.ApprovalRungChat && !o.Disabled
			})
			if task < 0 {
				t.Fatalf("no enabled chat lease among %+v", offers)
			}
			for _, offer := range offers {
				if !offer.Disabled && offer.Scope != hitl.ApprovalGrantScopeChat {
					t.Errorf("offer %s outlives the task", offer.ID)
				}
			}
			if len(hitl.QuietOptions(action, result.Decision, nil, nil)) != 0 {
				t.Fatal("agent policy offered quiet")
			}
			plan, err := hitl.CompileCheckpointApprovalPlan(hitl.CheckpointRequest{
				Kind: api.CheckpointKindToolApproval, ProposedAction: &action, Decision: result.Decision,
				GrantOffers: offers, SessionID: "task",
			})
			testutil.FailErr(t, "compile plan", err)
			face := slices.IndexFunc(plan.Options, func(o hitl.ApprovalOption) bool { return o.ID == plan.RecommendedOptionID })
			if face < 0 || plan.Options[face].Rung != hitl.ApprovalRungChat {
				t.Fatalf("%s face = %+v, want the chat lease", posture, plan.Options)
			}
		})
	}
}

// A Balanced chat lease covers its trust surface for the rest of the chat,
// never another surface; a Strict lease covers only its files.
func TestAgentPolicyLeaseCoversItsSubject(t *testing.T) {
	for _, tc := range []struct {
		posture   gate.Posture
		sameFile  bool
		sameKind  bool
		otherKind bool
	}{
		{gate.PostureBalanced, true, true, false},
		{gate.PostureStrict, true, false, false},
	} {
		t.Run(string(tc.posture), func(t *testing.T) {
			approvalGate, project := agentPolicyGate(t, tc.posture)
			first := agentPolicyWrite(t, project, "AGENTS.md")
			result, err := approvalGate.Evaluate(t.Context(), first)
			testutil.FailErr(t, "evaluate first", err)
			var lease *hitl.ApprovalGrantOffer
			for _, offer := range approvalGate.GrantOffers(first, result) {
				if offer.Rung == hitl.ApprovalRungChat && !offer.Disabled {
					lease = &offer
				}
			}
			if lease == nil {
				t.Fatal("no chat lease offered")
			}
			created, err := approvalGate.ApplyGrant(lease.Grant)
			testutil.FailErr(t, "apply lease", err)
			if !created {
				t.Fatal("chat lease was not installed")
			}
			for rel, covered := range map[string]bool{
				"AGENTS.md":                           tc.sameFile,
				"pkg/deep/AGENTS.md":                  tc.sameKind,
				".paintedwolf/skills/review/SKILL.md": tc.otherKind,
			} {
				result, err := approvalGate.Evaluate(t.Context(), agentPolicyWrite(t, project, rel))
				testutil.FailErr(t, "evaluate "+rel, err)
				if asksAgentPolicy(result) == covered {
					t.Errorf("%s: lease covered=%v, asked=%v", rel, covered, asksAgentPolicy(result))
				}
			}
		})
	}
}

func TestIndexOnlyInstructionChangeDoesNotEditInstructions(t *testing.T) {
	approvalGate, project := agentPolicyGate(t, gate.PostureStrict)
	result, err := approvalGate.Evaluate(t.Context(), hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "git_restore",
},
Scope: hitl.ActionScope{
ProjectDir: project,
},
Mutations: hitl.ActionMutations{
FileChanges: []api.ApprovalFileChange{{Path: "AGENTS.md", Target: "index"}},
},
})
	testutil.FailErr(t, "evaluate index-only change", err)
	if asksAgentPolicy(result) {
		t.Fatal("staging existing bytes asked to change instructions")
	}
}
