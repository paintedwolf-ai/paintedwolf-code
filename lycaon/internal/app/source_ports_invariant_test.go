package app

import (
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestBuildSourceInvocationRecordsAndReadsOneDurableHistory(t *testing.T) {
	testutil.SkipIfShort(t, "assembles the production source and session graph")
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-source-ports")
	app, err := Build(t.Context(), testBuildConfig(t, configlayout.FindModuleRoot()))
	testutil.FailErr(t, "build source graph", err)
	t.Cleanup(func() { _ = app.Close() })
	testdbseed.InsertProjectRoot(t, app.DB, testdbseed.DefaultProjectID, t.TempDir())
	sess, err := app.Sessions.Manager.Chats.CreateForProject(t.Context(), testdbseed.DefaultProjectID, wire.SessionPostureBuild)
	testutil.FailErr(t, "create source session", err)
	invocation, err := app.Sessions.Manager.ToolContext.Build(t.Context(), sess, "implement", inject.Machine{})
	testutil.FailErr(t, "build source invocation", err)
	source := invocation.Source
	if source.SourceLedger == nil || source.History.Files == nil || source.History.Comparison == nil ||
		source.History.Git == nil || source.History.Authorship == nil || source.Commands == nil ||
		source.GitMutations == nil || source.Observations == nil || source.SourceMutations == nil {
		t.Fatal("production invocation lacks explicit source services")
	}
	const path, content = "source-binding.txt", "retained source binding\n"
	testutil.FailErr(t, "record external source observation", source.SourceLedger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID: sess.ProjectID, RootID: source.ActiveRootID, Path: path,
		BranchID: source.ProjectSourceBranch, Op: wire.SourceChangeOpWrite,
		Origin: wire.SourceChangeOriginExternal, After: []byte(content),
	}))
	head, err := source.History.Files.ResolveHead(t.Context(), sess.ProjectID, source.ProjectSourceBranch, source.ActiveRootID, path)
	testutil.FailErr(t, "resolve invocation history", err)
	version, err := source.History.Files.ReadRestorableVersion(t.Context(), sess.ProjectID, head.VersionID)
	testutil.FailErr(t, "read invocation retained bytes", err)
	if string(version.Content) != content {
		t.Fatalf("recording and history ports disagree: retained=%q", version.Content)
	}
}
