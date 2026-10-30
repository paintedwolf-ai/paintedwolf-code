package toolfixture

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProgramFixture runs public command programs against the real native owners,
// profile policy, SQL receipts and captured output. Callers supply their roots.
type ProgramFixture struct {
	Host  *toolhost.Runtime
	Seed  *programtest.Fixture
	calls int
}

func NewProgramFixture(t *testing.T) *ProgramFixture {
	t.Helper()
	seed := programtest.New(t, programtest.Options{})
	host := ContractToolRuntime(t)
	testutil.FailErr(t, "compose contract command services", host.Commands.SetProgramServices(toolhost.ProgramServices{
		Recorder: seed.Recorder, Blobs: seed.Runtime.Blobs, Projector: seed.Runtime.Projector,
		Progress: seed.Runtime.Progress, Sessions: seed.Runtime.Sessions,
	}))
	return &ProgramFixture{Host: host, Seed: seed}
}

// Invoke preserves the supplied scope and profile while supplying the actual
// parent model receipt and session ownership required by the command entry.
func (f *ProgramFixture) Invoke(t *testing.T, source string, tc tools.ToolContext) (command.ProgramOutcome, error) {
	t.Helper()
	f.calls++
	callID := fmt.Sprintf("contract-program-%d", f.calls)
	args := map[string]any{"command": source}
	contract, _ := toolcontract.Lookup("command")
	parent, err := f.Seed.Recorder.Begin(t.Context(), invocation.Start{ProjectID: testdbseed.DefaultProjectID, SessionID: f.Seed.Session, ToolCallID: callID, ToolName: "command", Args: args, Contract: contract})
	testutil.FailErr(t, "begin command contract receipt", err)
	tc.Identity.ProjectID, tc.Identity.SessionID, tc.Identity.ToolCallID = testdbseed.DefaultProjectID, f.Seed.Session, callID
	if tc.Identity.Agent == "" {
		tc.Identity.Agent = "implement"
	}
	tc.Invocation.ID = parent.ID
	tc.Source.SourceWorkspaceKind = api.SourceWorkspaceKindProject
	tc.Host.SessionScratchDir, tc.Host.SessionOutputDir = f.Seed.ScratchDir, f.Seed.OutputDir
	text, err := f.Host.Executor.Invoke(t.Context(), "command", args, tc)
	var outcome command.ProgramOutcome
	if err != nil {
		return outcome, err
	}
	testutil.FailErr(t, "decode command contract outcome", json.Unmarshal([]byte(text), &outcome))
	return outcome, nil
}
