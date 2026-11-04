package native

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/pkg/api"
)

// A generated file stages with the session's work because the session's own
// program was observed creating it; a peer's dirty file stays out.
func TestGitCommitStagesWhatTheSessionsProgramGeneratedACCW10(t *testing.T) {
	repochange.DisableWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	f := programtest.New(t, programtest.Options{})
	ledger := f.AttachLedger(t)
	manager := git.NewManager()
	ledger.Git.SetGitReader(walkGitReader{manager})
	gittest.Init(t, f.Dir)
	testutil.FailErr(t, "write tracked file", os.WriteFile(filepath.Join(f.Dir, "README"), []byte("seed\n"), 0o644))
	gittest.CommitAll(t, f.Dir, "Initial")
	testutil.FailErr(t, "write peer file", os.WriteFile(filepath.Join(f.Dir, "peer.txt"), []byte("peer\n"), 0o644))

	raw, err := f.Call(t, t.Context(), `external -- /bin/sh -c 'echo generated > gen.txt'`)
	testutil.FailErr(t, "run generator", err)
	var outcome struct {
		Status       int `json:"status"`
		SourceWindow *struct {
			ID string `json:"id"`
		} `json:"source_window"`
	}
	testutil.FailErr(t, "decode program outcome", json.Unmarshal([]byte(raw), &outcome))
	if outcome.Status != 0 || outcome.SourceWindow == nil {
		t.Fatalf("generator outcome %s", raw)
	}
	walk, err := ledger.Walk.QueryWalk(t.Context(), testdbseed.DefaultProjectID, sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: f.Session}, 10, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "read session walk", err)
	var generated *sourceledger.Effect
	for _, file := range walk.Files {
		for i := range file.Effects {
			if file.Effects[i].Path == "gen.txt" {
				generated = &file.Effects[i]
			}
		}
	}
	if generated == nil || generated.SessionID != f.Session || generated.Cause != sourceledger.CauseCommandWindow || generated.CommandWindowID != outcome.SourceWindow.ID || generated.Origin != api.SourceChangeOriginExternal {
		t.Fatalf("generated effect = %+v", generated)
	}

	tc := nativefixture.Context(f.Dir)
	tc.Identity.ProjectID, tc.Identity.SessionID, tc.Identity.ToolCallID = testdbseed.DefaultProjectID, f.Session, "commit-call"
	tc.Invocation.ToolName = "git_commit"
	tc.Source.SourceLedger = ledger
	tc.Source.History = tools.SourceHistory{Files: ledger.History, Comparison: ledger.Comparisons, Git: ledger.Git, Authorship: ledger.Walk}
	tc.Source.Commands, tc.Source.Observations, tc.Source.GitMutations = ledger.Commands, ledger.Inventory, ledger.Git
	tool := &GitCommitTool{Git: manager, Boundary: nativefixture.Boundary(t)}
	_, err = tool.Run(t.Context(), map[string]any{"message": "Generate"}, tc)
	testutil.FailErr(t, "commit session work", err)
	committed := strings.Fields(gittest.Run(t, f.Dir, "show", "--name-only", "--format=", "HEAD"))
	if !slices.Contains(committed, "gen.txt") || slices.Contains(committed, "peer.txt") {
		t.Fatalf("committed paths = %v, want gen.txt without peer.txt", committed)
	}
}
