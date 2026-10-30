package contract

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/pkg/api"
)

type securityProgramFixture struct {
	host  *toolhost.Runtime
	seed  *programtest.Fixture
	calls int
}

func newSecurityProgramFixture(t *testing.T) *securityProgramFixture {
	t.Helper()
	seed := programtest.New(t, programtest.Options{})
	host := securityNativeRuntime(t)
	testutil.FailErr(t, "compose real native program owners", host.Commands.SetProgramServices(toolhost.ProgramServices{
		Recorder: seed.Recorder, Blobs: seed.Runtime.Blobs, Projector: seed.Runtime.Projector,
		Progress: seed.Runtime.Progress, Sessions: seed.Runtime.Sessions,
	}))
	return &securityProgramFixture{host: host, seed: seed}
}

func (f *securityProgramFixture) run(t *testing.T, profile, source string) command.ProgramOutcome {
	t.Helper()
	f.calls++
	callID := fmt.Sprintf("security-program-%d", f.calls)
	args := map[string]any{"command": source}
	contract, _ := toolcontract.Lookup("command")
	parent, err := f.seed.Recorder.Begin(t.Context(), invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: f.seed.Session, ToolCallID: callID,
		ToolName: "command", Args: args, Contract: contract,
	})
	testutil.FailErr(t, "begin actual command model receipt", err)
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: profile, ProjectID: testdbseed.DefaultProjectID, SessionID: f.seed.Session, ToolCallID: callID},
		Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: f.seed.Dir, IsPrimary: true}}, ActiveRootID: "r1", SourceWorkspaceKind: api.SourceWorkspaceKindProject},
	}
	tc.Invocation.ID = parent.ID
	text, err := f.host.Executor.Invoke(t.Context(), "command", args, tc)
	testutil.FailErr(t, "invoke configured command program", err)
	var outcome command.ProgramOutcome
	testutil.FailErr(t, "decode settled program outcome", json.Unmarshal([]byte(text), &outcome))
	if outcome.ProgramID == "" || outcome.State != "completed" || outcome.Status != 0 || outcome.NativeGaps != 0 || outcome.ExecutedChildren == 0 || outcome.Refusal != nil {
		t.Fatalf("native program did not complete through its owners: %+v", outcome)
	}
	if programs := f.seed.Programs(t); len(programs) != f.calls || programs[len(programs)-1] != "completed" {
		t.Fatalf("actual program receipt states = %v", programs)
	}
	return outcome
}
