package contract

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
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

// A bisect check is one of the program's external processes, so the window is
// active while it runs.
func TestGitBisectRunHoldsTheWindowWhileItsCheckRuns(t *testing.T) {
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
	tc := gitProgramContext(dir)
	tc.Identity.Agent = ""
	tc.Source.Commands = ledger.Commands
	done := make(chan error, 1)
	go func() {
		_, err := f.Invoke(t, `git bisect start HEAD HEAD~2 && git bisect run /bin/sh -c 'touch started; until test -e release; do sleep 0.01; done; exit 0'
git bisect reset`, tc)
		done <- err
	}()
	testutil.AwaitCondition(t, "the check started", func() bool {
		_, err := os.Stat(filepath.Join(dir, "started"))
		return err == nil
	})
	contractcheck.FailErr(t, "drift during the check", os.WriteFile(filepath.Join(dir, "during.txt"), []byte("live"), 0o644))
	contractcheck.FailErr(t, "pass during the check", ledger.Inventory.EnsureInventory(t.Context(), sourceledger.InventoryRequest{
		ProjectID: testdbseed.DefaultProjectID, Roots: []sourceledger.RootSpec{{ID: "r1", Path: dir}}, Force: true, Wait: true,
	}))
	walk, err := ledger.Walk.QueryWalk(t.Context(), testdbseed.DefaultProjectID, sourceledger.Baseline{}, 200, 0, sourceledger.CommitLens{})
	contractcheck.FailErr(t, "read walk", err)
	attributed := false
	for _, file := range walk.Files {
		for _, effect := range file.Effects {
			attributed = attributed || (effect.Path == "during.txt" && effect.CommandWindowID != "")
		}
	}
	if !attributed {
		t.Fatalf("drift during the check not attributed: %+v", walk.Files)
	}
	contractcheck.FailErr(t, "release the check", os.WriteFile(filepath.Join(dir, "release"), nil, 0o644))
	contractcheck.FailErr(t, "run bisect", testutil.AwaitValue(t, "bisect result", done))
}
