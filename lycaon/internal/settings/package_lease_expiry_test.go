package settings

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/packageexec"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A chat-carried package lease stops covering at its deadline without a restart.
func TestPackageCoordinateChatLeaseExpires(t *testing.T) {
	store, err := NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	testutil.FailErr(t, "PutGlobal balanced", store.PutGlobal(ApprovalConfig{Posture: gate.PostureBalanced}))
	approvalGate := NewRuleApprovalGate(store, NoSources())

	execution := &packageexec.Execution{
		Manager:   "npm",
		Operation: packageexec.OperationDependencyInstall,
		Packages: []packageexec.Package{
			{System: "npm", Name: "lodash", RequestedVersion: "4.17.21", Status: packageexec.IdentityResolved},
		},
	}
	expired := time.Now().Add(-time.Minute)
	grant := hitl.ApprovalGrant{
		ID:            "grant-pkg-day",
		Scope:         hitl.ApprovalGrantScopeChat,
		ChatSessionID: "chat-task-1",
		ProjectID:     "proj-1",
		ExpiresAt:     &expired,
		Witness:       hitl.BoundaryWitness(hitl.Contained{}),
		Predicate:     hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryPackageCoordinate, Pattern: hitl.PackageCoordinatePattern(execution)},
	}
	_, err = approvalGate.ApplyGrant(grant)
	testutil.FailErr(t, "apply expired package coordinate grant", err)

	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
Scope: hitl.ActionScope{
SessionID: "chat-task-1",
ProjectID: "proj-1",
},
Presentation: hitl.ActionPresentation{
Command: "npm install lodash@4.17.21",
},
Execution: hitl.ActionExecution{
PackageExecution: execution,
},
}
	if approvalGate.GrantCovers(action) {
		t.Fatal("an expired chat package lease must not cover the action")
	}
	if grants := approvalGate.ListGrants("chat-task-1"); len(grants) != 0 {
		t.Fatalf("expired lease still listed: %+v", grants)
	}
}
