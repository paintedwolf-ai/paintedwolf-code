package contract

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/survey"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

type countingWindowOpener struct {
	sourceledger.CommandWindowOpener
	opens atomic.Int32
}

func (o *countingWindowOpener) OpenCommandWindow(ctx context.Context, in sourceledger.CommandWindowInput) (*sourceledger.OpenCommandWindow, error) {
	o.opens.Add(1)
	return o.CommandWindowOpener.OpenCommandWindow(ctx, in)
}

// windowProgramRepo runs public programs in a repository with three commits,
// over real native owners and a real source ledger.
func windowProgramRepo(t *testing.T) (*toolfixture.ProgramFixture, *countingWindowOpener) {
	t.Helper()
	repochange.DisableWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	f := toolfixture.NewProgramFixture(t)
	ledger := f.Seed.AttachLedger(t)
	dir := f.Seed.Dir
	gittest.Init(t, dir)
	for _, content := range []string{"one\n", "two\n", "three\n"} {
		contractcheck.FailErr(t, "write tracked file", os.WriteFile(filepath.Join(dir, "a.txt"), []byte(content), 0o644))
		gittest.CommitAll(t, dir, "Change")
	}
	return f, &countingWindowOpener{CommandWindowOpener: ledger.Commands}
}

func TestProgramWindowACCW08NativeProgramOpensNone(t *testing.T) {
	f, opener := windowProgramRepo(t)
	tc := gitProgramContext(f.Seed.Dir)
	tc.Identity.Agent = ""
	tc.Source.Commands = opener
	out, err := f.Invoke(t, `cat a.txt; grep three a.txt; git status --porcelain; sed -i s/three/four/ a.txt; echo done`, tc)
	contractcheck.FailErr(t, "invoke native program", err)
	if out.Status != 0 || out.Refusal != nil || out.SourceWindow != nil {
		t.Fatalf("native outcome = %+v", out)
	}
	if opens := opener.opens.Load(); opens != 0 {
		t.Fatalf("native window opens = %d, want 0", opens)
	}
	edited, err := os.ReadFile(filepath.Join(f.Seed.Dir, "a.txt"))
	contractcheck.FailErr(t, "read edited file", err)
	if string(edited) != "four\n" {
		t.Fatalf("sed -i result = %q", edited)
	}
}

// Host utilities that run a command operand dispatch it through the program's
// own launch door, so nesting never opens a second window.
func TestProgramWindowNestedDispatchOpensOne(t *testing.T) {
	f, opener := windowProgramRepo(t)
	tc := gitProgramContext(f.Seed.Dir)
	tc.Identity.Agent = ""
	tc.Source.Commands = opener
	out, err := f.Invoke(t, `printf 'x\ny\n' | xargs -n1 /bin/sh -c 'echo $0 > $0.txt'
env MARK=1 /bin/sh -c 'echo c > c.txt'
git bisect start HEAD HEAD~2 && git bisect run /bin/sh -c 'exit 0'
git bisect reset`, tc)
	contractcheck.FailErr(t, "invoke nested program", err)
	if out.Refusal != nil || out.SourceWindow == nil {
		t.Fatalf("nested outcome = %+v", out)
	}
	if opens := opener.opens.Load(); opens != 1 {
		t.Fatalf("nested window opens = %d, want 1", opens)
	}
	for _, name := range []string{"x.txt", "y.txt", "c.txt"} {
		if _, err := os.Stat(filepath.Join(f.Seed.Dir, name)); err != nil {
			t.Fatalf("nested command did not run: %v (%+v)", err, out)
		}
	}
}

// A program's window has settled by the time the overview reads, so its effects
// list with their window. A window is observation, not authorship, so the actor stays external.
func TestSourceHistoryOverviewACCW02ProgramEffectsListSettled(t *testing.T) {
	repochange.DisableWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	f := toolfixture.NewProgramFixture(t)
	ledger := f.Seed.AttachLedger(t)
	dir := f.Seed.Dir
	gittest.Init(t, dir)
	contractcheck.FailErr(t, "write tracked file", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644))
	gittest.CommitAll(t, dir, "Start")

	tc := gitProgramContext(dir)
	tc.Identity.Agent = ""
	tc.Identity.ProjectID, tc.Identity.SessionID = testdbseed.DefaultProjectID, f.Seed.Session
	tc.Source.Commands = ledger.Commands
	tc.Source.History = tools.SourceHistory{Files: ledger.History, Comparison: ledger.Comparisons, Git: ledger.Git, Authorship: ledger.Walk, Windows: ledger.Commands}
	_, err := ledger.Checkpoints.CreateStructuralCheckpoint(t.Context(), sourceledger.StructuralCheckpointInput{
		ProjectID: tc.Identity.ProjectID, Kind: sourceledger.CheckpointTurn, Label: "Turn start", SessionID: tc.Identity.SessionID, Turn: 1,
	})
	contractcheck.FailErr(t, "create turn checkpoint", err)

	out, err := f.Invoke(t, `/bin/sh -c 'echo made > gen.txt'`, tc)
	contractcheck.FailErr(t, "invoke generator", err)
	if out.Status != 0 || out.SourceWindow == nil {
		t.Fatalf("generator outcome = %+v", out)
	}
	tool := &survey.SourceHistoryTool{Boundary: f.Seed.Boundary}
	raw, err := tool.Run(t.Context(), map[string]any{}, tc)
	contractcheck.FailErr(t, "source_history()", err)
	var overview struct {
		Files []struct {
			Path     string `json:"path"`
			Actor    string `json:"actor"`
			WindowID string `json:"window_id"`
			Settled  *bool  `json:"settled"`
			During   bool   `json:"during_your_command"`
		} `json:"files"`
		OpenWindows []struct {
			ID string `json:"id"`
		} `json:"open_windows"`
	}
	contractcheck.FailErr(t, "decode overview", json.Unmarshal([]byte(raw), &overview))
	if len(overview.OpenWindows) != 0 {
		t.Fatalf("open windows = %+v, want the program settled", overview.OpenWindows)
	}
	for _, file := range overview.Files {
		if file.Path == "gen.txt" {
			if file.WindowID != out.SourceWindow.ID || file.Settled == nil || !*file.Settled || file.Actor != "external" || !file.During {
				t.Fatalf("generated row = %+v, want an observed change in settled window %s", file, out.SourceWindow.ID)
			}
			return
		}
	}
	t.Fatalf("overview rows = %+v, want gen.txt", overview.Files)
}
