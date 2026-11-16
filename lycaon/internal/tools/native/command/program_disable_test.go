package command_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func newProgramFixtureWith(t *testing.T, structured map[string]tools.ToolHandler) *programFixture {
	t.Helper()
	f := newProgramFixture(t)
	registry := tools.NewDefaultRegistry()
	for _, name := range []string{"write", "read"} {
		handler := structured[name]
		if handler == nil {
			handler = func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				return "", errors.New("structured tool is not exercised")
			}
		}
		testutil.FailErr(t, "register "+name, registry.Register(name, handler))
	}
	classes, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandExternalExec)
	testutil.FailErr(t, "compile profile effects", err)
	offered := map[string]bool{}
	for name := range structured {
		offered[name] = true
	}
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "fixture", Tools: offered, CommandEffects: classes}})
	f.Runtime.Registry = registry
	f.Runtime.Boundary = boundary
	f.Runtime.Executor = toolexecution.NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), registry, "fixture")
	return f
}

func (f *programFixture) modelCall(t *testing.T, tool string, args map[string]any) tools.ToolContext {
	t.Helper()
	callID := fmt.Sprintf("model-call-%s", tool)
	contract, _ := toolcontract.Lookup(tool)
	parent, err := f.Recorder.Begin(t.Context(), invocation.Start{ProjectID: testdbseed.DefaultProjectID, SessionID: f.Session, ToolCallID: callID, ToolName: tool, Args: args, Contract: contract})
	testutil.FailErr(t, "begin model receipt", err)
	tc := tools.ToolContext{Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: f.Dir, IsPrimary: true}}, ActiveRootID: "r1", SourceWorkspaceKind: api.SourceWorkspaceKindProject}}
	tc.Identity.Agent, tc.Identity.ProjectID, tc.Identity.SessionID, tc.Identity.ToolCallID = "fixture", testdbseed.DefaultProjectID, f.Session, callID
	tc.Invocation.ID = parent.ID
	tc.Host.SessionScratchDir, tc.Host.SessionOutputDir = f.ScratchDir, f.OutputDir
	return tc
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	testutil.FailErr(t, "read "+path, err)
	return string(data)
}
