package contract

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Every file a command plan writes through a stream reaches the approval gate
// as a prepared change, and the gate judges that change as it judges the same
// change from the write tool: pointed at a credential file, both ask.
func TestCommandRedirectTargetsAreReviewedLikeNativeWrites(t *testing.T) {
	t.Parallel()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, profiles)
	registry := tools.NewDefaultRegistry()
	background := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	command := &native.CommandTool{Runner: hostcmd.NewRunner(), Boundary: boundary, Background: background}
	verify := &native.VerifyTool{Runner: hostcmd.NewRunner(), Boundary: boundary, Background: background}
	contractcheck.FailErr(t, "register command", registry.Register("command", command.Run))
	contractcheck.FailErr(t, "register verify", registry.Register("verify", verify.Run))
	recorder := &recordingGate{}
	executor := tools.NewDefaultToolExecutor(tools.NewApprovalPolicyEngine(tools.NewProfilePolicyEngine(boundary), recorder), registry, "implement")
	// Prepared changes reach the gate the executor was given; the recorder
	// never asks, so no checkpoint manager is needed.
	executor.SetCheckpointManager(nil, recorder)
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	contractcheck.FailErr(t, "approval store", err)
	realGate := settings.NewRuleApprovalGate(store, settings.NoSources())

	wrote := 0
	for _, tool := range []string{"command", "verify"} {
		for _, tc := range commandPlanCorpus() {
			root := t.TempDir()
			seedCommandTree(t, root)
			root, err := filepath.EvalSymlinks(root)
			contractcheck.FailErr(t, "resolve root", err)
			before := snapshotTree(t, root)
			recorder.reset()
			_, err = executor.Invoke(t.Context(), tool, cloneArgs(tc.args).(map[string]any), tools.ToolContext{
				Roots:        []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}},
				ActiveRootID: "root", ProjectID: "project", SourceWorkspaceKind: api.SourceWorkspaceKindProject,
				SessionID: "chat", ToolCallID: "call-" + tool, Agent: "implement", SessionScratchDir: t.TempDir(),
			})
			contractcheck.FailErr(t, tool+" "+tc.name, err)

			for _, written := range changedFiles(t, root, before) {
				wrote++
				review, ok := changeReviewFor(recorder, tool, written)
				if !ok {
					t.Errorf("%s %s wrote %s without a prepared-change review", tool, tc.name, relTo(root, written))
					continue
				}
				credential := filepath.Join(filepath.Dir(written), ".env")
				asCommand := retarget(review, credential)
				asWrite := retarget(review, credential)
				asWrite.Tool, asWrite.Args = "write", map[string]any{"path": credential, "content": ""}
				commandResult, err := realGate.Evaluate(t.Context(), asCommand)
				contractcheck.FailErr(t, "evaluate command review", err)
				writeResult, err := realGate.Evaluate(t.Context(), asWrite)
				contractcheck.FailErr(t, "evaluate write review", err)
				if !writeResult.Required() {
					t.Fatalf("the write tool's review of %s did not ask; the comparison checks nothing", credential)
				}
				if !commandResult.Required() || !slices.Contains(commandResult.Decision.Gates(), writeResult.Gate()) {
					t.Errorf("%s %s: a stream write to %s asks %v, the same write from the write tool asks %q",
						tool, tc.name, relTo(root, credential), gatesOf(commandResult), writeResult.Gate())
				}
			}
		}
	}
	if wrote == 0 {
		t.Fatal("the corpus wrote no stream files; the invariant checked nothing")
	}
}

// changeReviewFor returns the prepared-change review that names path.
func changeReviewFor(g *recordingGate, tool, path string) (hitl.ProposedAction, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	want := fspath.CanonicalPath(path)
	for _, action := range g.actions {
		if action.Tool != tool || len(action.FileChanges) == 0 {
			continue
		}
		if slices.ContainsFunc(action.Files, func(f string) bool { return fspath.CanonicalPath(f) == want }) {
			return action, true
		}
	}
	return hitl.ProposedAction{}, false
}

// retarget points a recorded review at another file of the same root.
func retarget(action hitl.ProposedAction, path string) hitl.ProposedAction {
	out := action
	out.Files, out.ResolvedFiles = []string{path}, []string{fspath.CanonicalPath(path)}
	out.FileChanges = []api.ApprovalFileChange{{Path: path, Operation: "write"}}
	out.AgentPolicy = nil
	return out
}

func gatesOf(result *hitl.ApprovalResult) []api.ApprovalGate {
	if result == nil || result.Decision == nil {
		return nil
	}
	return result.Decision.Gates()
}
