package settings_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRecoveryLossRequiresExactApprovalAtEveryPosture(t *testing.T) {
	for _, posture := range []gate.Posture{gate.PostureLight, gate.PostureBalanced, gate.PostureStrict} {
		t.Run(string(posture), func(t *testing.T) {
			stageBundledApprovalsPosture(t, posture)
			root := t.TempDir()
			store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
			testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
			approval := settings.NewRuleApprovalGate(store, settings.NoSources())
			action := hitl.ProposedAction{
				Invocation: hitl.ActionInvocation{Tool: "delete", ActionID: "first-removal", Files: []string{filepath.Join(root, "build")}},
				Scope:      hitl.ActionScope{ProjectDir: root, ProjectID: "project", SessionID: "session"},
				Execution:  hitl.ActionExecution{Contained: hitl.Contained{FSJailed: true, Egress: gate.EgressProxy, Roots: []string{root}, WriteRoots: []string{root}}},
				Mutations:  hitl.ActionMutations{RecoveryLoss: true, FileChanges: []api.ApprovalFileChange{{Path: "build", Operation: "delete", BeforeBytes: 64}}},
			}
			result, err := approval.Evaluate(t.Context(), action)
			testutil.FailErr(t, "approval.Evaluate failed", err)
			if !result.Required() || result.Decision.Primary != api.GateExplicitApprovalRequest {
				t.Fatalf("permanent removal did not require exact consent: %+v", result)
			}
			if settings.ClassifyTier(action) != settings.TierIrreversible {
				t.Fatal("permanent removal classified recoverable")
			}
			before := hitl.GrantKey(action)
			action.Invocation.ActionID = "second-removal"
			if before == "" || before == hitl.GrantKey(action) {
				t.Fatal("permanent removal reused another operation's identity")
			}
			action.Mutations.RecoveryLoss = false
			action.Invocation.ActionID = "first-removal"
			if before == hitl.GrantKey(action) {
				t.Fatal("recoverable authority covers permanent removal")
			}
		})
	}
}
