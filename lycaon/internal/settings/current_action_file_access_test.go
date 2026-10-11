package settings_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestApprovalFreezesExactFileAccess(t *testing.T) {
	stageBundledApprovals(t, nil)
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	testutil.FailErr(t, "create approval store", err)
	testutil.FailErr(t, "PutGlobal strict", store.PutGlobal(settings.ApprovalConfig{Posture: gate.PostureStrict}))
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	project := t.TempDir()
	outside := filepath.Join(filepath.VolumeName(project)+string(filepath.Separator), "unattached", t.Name())
	for _, tc := range []struct {
		tool  string
		write bool
	}{
		{"read", false}, {"write", true},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			paths := []string{filepath.Join(outside, "a.txt"), filepath.Join(outside, "b.txt")}
			result, err := approvalGate.Evaluate(context.Background(), hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: tc.tool,
Files: paths,
},
Scope: hitl.ActionScope{
ProjectDir: project,
SessionID: "review-paths",
},
})
			testutil.FailErr(t, "evaluate file action", err)
			if !result.Required() || len(result.FileAccess) != len(paths) {
				t.Fatalf("approval did not retain every target: %+v", result)
			}
			for i, access := range result.FileAccess {
				if access.Path != grantedpath.Normalize(paths[i]) || access.Write != tc.write || access.Tree {
					t.Fatalf("target %d authority = %+v", i, access)
				}
			}
		})
	}
}
