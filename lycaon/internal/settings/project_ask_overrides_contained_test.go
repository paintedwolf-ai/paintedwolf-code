package settings_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Containment auto-approve is a safety floor, not a ceiling: an explicit project
// rule asking for confirmation on a tool must still pend, even when the kernel
// would contain the effect. Without this, a user cannot opt into stricter
// behaviour than the boundary gives them.
func TestProjectAskRuleOverridesContainedAutoApprove(t *testing.T) {
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "load approval store", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())

	proj := t.TempDir()
	overlayDir := filepath.Join(proj, settingsoverlay.DirName())
	testutil.FailErr(t, "mkdir overlay", os.MkdirAll(overlayDir, 0o700))
	const overlay = "rules:\n  - category: tool\n    pattern: write\n    effect: ask\n"
	testutil.FailErr(t, "write overlay",
		os.WriteFile(filepath.Join(overlayDir, "approvals.yaml"), []byte(overlay), 0o600))

	contained := hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{proj}}
	res, err := gate.Evaluate(context.Background(), hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "write",
Args: map[string]any{"path": "a.txt", "content": "x"},
Files: []string{filepath.Join(proj, "a.txt")},
},
Scope: hitl.ActionScope{
ProjectDir: proj,
},
Execution: hitl.ActionExecution{
Contained: contained,
},
})
	testutil.FailErr(t, "evaluate write", err)
	if !res.Required() {
		t.Fatalf("project ask rule must pend a contained in-project write: %#v", res)
	}
}
