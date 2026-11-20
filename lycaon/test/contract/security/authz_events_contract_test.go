package contract

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// authzEventsReadThroughRuleGate runs one in-project read through the real
// rule gate with the SQL authz ledger wired and returns the session's rows.
func authzEventsReadThroughRuleGate(t *testing.T, profiles []sandbox.ToolProfile) []authzcontext.Event {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "authz-rule-gate.db")
	const sessionID = "sess-authz-rule-gate"
	testdbseed.InsertSession(t, sqlDB, sessionID, testdbseed.DefaultProjectID)

	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	contractcheck.FailErr(t, "approval store", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, profiles)
	policy := toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate)
	reg := tools.NewDefaultRegistry()
	contractcheck.FailErr(t, "register read", reg.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return "ok", nil
	}))
	exec := toolexecution.NewExecutor(policy, reg, "implement")
	exec.Approvals.SetAuthzRecorder(authzcontext.SQLRecorder(sqlDB))

	root := t.TempDir()
	_, _ = exec.Invoke(t.Context(), "read", map[string]any{"path": filepath.Join(root, "a.go")}, tools.ToolContext{
		Source: tools.InvocationSource{
			Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1",
		},
		Identity: tools.InvocationIdentity{SessionID: sessionID, Agent: "implement"},
	})
	rows, err := authzcontext.NewSQLStore(sqlDB).ListEvents(t.Context(), sessionID)
	contractcheck.FailErr(t, "list authz events", err)
	return rows
}

// Silent auto-allows append no ledger rows; an executor policy block does.
func TestRuleGateDoesNotAppendAuthzEvents(t *testing.T) {
	t.Parallel()
	if rows := authzEventsReadThroughRuleGate(t, outsideReadProfiles(t)); len(rows) != 0 {
		t.Fatalf("auto-allowed read appended authz rows: %+v", rows)
	}

	denied := outsideReadProfiles(t)
	for i := range denied {
		denied[i].DenyTools = append(denied[i].DenyTools, "read")
	}
	rows := authzEventsReadThroughRuleGate(t, denied)
	if len(rows) != 1 || rows[0].Action != authzcontext.EventActionToolDenied || rows[0].ResolvedBy != authzcontext.ResolvedBySystemDeny {
		t.Fatalf("profile-denied read rows = %+v, want one tool_denied", rows)
	}
}
