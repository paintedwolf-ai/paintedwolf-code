package native

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOutsideWritesRecordInTheOutsideNamespace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the outside namespace covers host paths with a single root")
	}
	root := t.TempDir()
	tctx := nativefixture.Context(root)
	tctx.Identity.ProjectID = "proj-outside"
	tctx.Identity.WorkerJobID = "job-1"
	tctx.Source.SourceWorkspaceKind = api.SourceWorkspaceKindWorker
	tctx.Source.OutsideRoot = projectroot.OutsideRef("outside-root")

	in, change, ok := agentCommitInputs(tctx, agentMutation{
		Op: api.SourceChangeOpWrite, AbsPath: "/opt/pw-fixture/app.conf", FromAbsPath: "/opt/pw-fixture/app.conf.bak",
	})
	if !ok {
		t.Fatal("an outside write was not recorded")
	}
	if in.RootID != "outside-root" || in.Path != "opt/pw-fixture/app.conf" || in.FromPath != "opt/pw-fixture/app.conf.bak" {
		t.Fatalf("outside location = %q %q from %q", in.RootID, in.Path, in.FromPath)
	}
	if in.BranchID != sourcebranch.Trunk {
		t.Fatalf("a worker's outside write recorded on branch %q", in.BranchID)
	}
	if change.RootID != "outside-root" || change.AbsPath != "/opt/pw-fixture/app.conf" {
		t.Fatalf("outside change = %+v", change)
	}
}

func TestProjectPathsAndProgramTempStayOutOfTheOutsideNamespace(t *testing.T) {
	root := t.TempDir()
	tctx := nativefixture.Context(root)
	tctx.Identity.ProjectID = "proj-outside"
	tctx.Source.OutsideRoot = projectroot.OutsideRef("outside-root")

	if in, _, ok := agentCommitInputs(tctx, agentMutation{Op: api.SourceChangeOpWrite, AbsPath: filepath.Join(root, "main.go")}); !ok || in.RootID == "outside-root" {
		t.Fatalf("a project write recorded as %q (ok=%v)", in.RootID, ok)
	}
	tctx.Host.SessionScratchDir = t.TempDir()
	if in, _, ok := agentCommitInputs(tctx, agentMutation{Op: api.SourceChangeOpWrite, AbsPath: filepath.Join(tctx.Host.SessionScratchDir, "notes.md")}); !ok || in.RootID != "outside-root" {
		t.Fatalf("a scratch write recorded as %q (ok=%v)", in.RootID, ok)
	}
	if _, _, ok := agentCommitInputs(tctx, agentMutation{Op: api.SourceChangeOpWrite, AbsPath: filepath.Join(enginepaths.ProgramTempUnder(tctx.Host.SessionScratchDir), "a.o")}); ok {
		t.Fatal("a program temp write entered source history")
	}
	tctx.Source.OutsideRoot = projectroot.RootRef{}
	if _, _, ok := agentCommitInputs(tctx, agentMutation{Op: api.SourceChangeOpWrite, AbsPath: "/opt/pw-fixture/app.conf"}); ok {
		t.Fatal("an outside write recorded without an outside namespace")
	}
}
