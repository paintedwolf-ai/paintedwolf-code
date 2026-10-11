package settings_test

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// scratchGate evaluates at one posture for a project that does not contain the
// temporary directory.
func scratchGate(t *testing.T, posture gate.Posture) (hitl.ApprovalGate, string) {
	t.Helper()
	stageBundledApprovalsYAML(t, "approval_posture: "+string(posture)+"\nrules:\n")
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	testutil.FailErr(t, "approval store", err)
	return settings.NewRuleApprovalGate(store, settings.NoSources()), t.TempDir()
}

func scratchAction(tool, project, path string) hitl.ProposedAction {
	return hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: tool,
Files: []string{path},
},
Scope: hitl.ActionScope{
ProjectID: "project",
ProjectDir: project,
SessionID: "task",
},
}
}

func asksOutsideRoots(result *hitl.ApprovalResult) bool {
	return result != nil && result.Required() &&
		(slices.Contains(result.Decision.Gates(), api.GateOutsideRootsWrite) || slices.Contains(result.Decision.Gates(), api.GateOutsideRootsRead))
}

func TestScratchWriteAsksOnlyAtStrict(t *testing.T) {
	for _, tc := range []struct {
		posture gate.Posture
		asks    bool
	}{
		{gate.PostureLight, false},
		{gate.PostureBalanced, false},
		{gate.PostureStrict, true},
	} {
		t.Run(string(tc.posture), func(t *testing.T) {
			approvalGate, project := scratchGate(t, tc.posture)
			scratch := filepath.Join(t.TempDir(), "probe")
			result, err := approvalGate.Evaluate(t.Context(), scratchAction("mkdir", project, scratch))
			testutil.FailErr(t, "evaluate", err)
			if asksOutsideRoots(result) != tc.asks {
				t.Fatalf("%s asks=%v, want %v: %+v", tc.posture, asksOutsideRoots(result), tc.asks, result)
			}
			if result.Denied {
				t.Fatalf("scratch write was refused: %+v", result)
			}
		})
	}
}

// Reads keep scratch authority at every posture; only the write is reviewed.
func TestScratchReadStaysSilentAtStrict(t *testing.T) {
	approvalGate, project := scratchGate(t, gate.PostureStrict)
	scratch := filepath.Join(t.TempDir(), "probe")
	result, err := approvalGate.Evaluate(t.Context(), scratchAction("read", project, scratch))
	testutil.FailErr(t, "evaluate", err)
	if result.Required() {
		t.Fatalf("scratch read asked at strict: %+v", result)
	}
}

// Subprocess write roots are the command boundary's own subject.
func TestScratchCommandStaysSilentAtStrict(t *testing.T) {
	approvalGate, project := scratchGate(t, gate.PostureStrict)
	scratch := filepath.Join(t.TempDir(), "probe")
	action := scratchAction("command", project, scratch)
	action.Invocation.Args = map[string]any{"command": "mkdir -p " + scratch}
	result, err := approvalGate.Evaluate(t.Context(), action)
	testutil.FailErr(t, "evaluate", err)
	if asksOutsideRoots(result) {
		t.Fatalf("command scratch write asked on the file gate: %+v", result)
	}
}
