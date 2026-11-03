package contract

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Native verification redirects receive the same prepared-change review as writes.
func TestVerificationRedirectTargetsAreReviewedLikeNativeWrites(t *testing.T) {
	f, recorder := verificationProgramFixture(t)
	_, err := f.Invoke(t, t.Context(), "verify", map[string]any{"command": "printf checked > result.txt"}, (&native.VerifyTool{Programs: f.Runtime}).Run)
	contractcheck.FailErr(t, "verification redirect", err)
	action, ok := changeReviewFor(recorder, "write", filepath.Join(f.Dir, "result.txt"))
	if !ok {
		t.Fatal("redirect omitted its prepared-change review")
	}
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	contractcheck.FailErr(t, "approval store", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	result, err := gate.Evaluate(t.Context(), retarget(action, filepath.Join(f.Dir, ".env")))
	contractcheck.FailErr(t, "protected destination review", err)
	if len(gatesOf(result)) == 0 {
		t.Fatal("protected destination received no approval gate")
	}
}

// changeReviewFor returns the prepared-change review that names path.
func changeReviewFor(g *recordingGate, tool, path string) (hitl.ProposedAction, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	want := fspath.CanonicalPath(path)
	for _, action := range g.actions {
		if action.Invocation.Tool != tool || len(action.Mutations.FileChanges) == 0 {
			continue
		}
		if slices.ContainsFunc(action.Invocation.Files, func(f string) bool { return fspath.CanonicalPath(f) == want }) {
			return action, true
		}
	}
	return hitl.ProposedAction{}, false
}

// retarget points a recorded review at another file of the same root.
func retarget(action hitl.ProposedAction, path string) hitl.ProposedAction {
	out := action
	out.Invocation.Files, out.Invocation.ResolvedFiles = []string{path}, []string{fspath.CanonicalPath(path)}
	out.Invocation.NativePathTargets = []hitl.NativePathTarget{{Path: path, Write: true}}
	out.Invocation.ApprovedFileAccess = nil
	out.Mutations.FileChanges = []api.ApprovalFileChange{{Path: path, Operation: "write"}}
	out.Mutations.AgentPolicy = nil
	return out
}

func gatesOf(result *hitl.ApprovalResult) []api.ApprovalGate {
	if result == nil || result.Decision == nil {
		return nil
	}
	return result.Decision.Gates()
}
