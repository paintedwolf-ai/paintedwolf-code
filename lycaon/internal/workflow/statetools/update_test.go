package statetools

import (
	"context"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

type updateCommands struct {
	runstate.CommandReceipts
	commits  []runstate.CommandMutation
	replayed bool
}

func (c *updateCommands) ReplayCommandOperation(context.Context, string, string, string) (*api.WorkflowRun, bool, error) {
	return &api.WorkflowRun{ID: "run"}, c.replayed, nil
}
func (c *updateCommands) CommitCommand(_ context.Context, _ *api.WorkflowRun, m runstate.CommandMutation) error {
	c.commits = append(c.commits, m)
	c.replayed = true
	return nil
}

type updateDirectories struct{ root string }

func (d updateDirectories) ProjectDirForRun(context.Context, *api.WorkflowRun) string { return d.root }

func TestStateUpdateCommitsCustomFactOnceUnderOperationReceipt(t *testing.T) {
	root := t.TempDir()
	commands := &updateCommands{}
	runs := queryRuns{active: &api.WorkflowRun{ID: "run", SessionID: "session", Status: api.WorkflowRunStatusRunning, Revision: 4}, vars: map[string]any{"retained": "unchanged"}}
	reg := tools.NewDefaultRegistry()
	deps := StateToolDeps{Runs: runs, Sessions: querySessions{root}, Vars: runstate.NewVariables(nil, nil, nil), Journal: &runstate.Journal{Commands: commands, Directories: updateDirectories{root}}}
	if err := RegisterStateTools(reg, deps); err != nil {
		t.Fatal(err)
	}
	ctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session", ToolCallID: "operation"}, Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}}}
	args := map[string]any{"path": "custom.answer", "value": "42"}
	first, err := reg.Run(t.Context(), "state_update", args, ctx)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := reg.Run(t.Context(), "state_update", args, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first != replay || len(commands.commits) != 1 {
		t.Fatalf("first=%q replay=%q commits=%v", first, replay, commands.commits)
	}
	mutation := commands.commits[0]
	if mutation.OperationID != "operation" || mutation.Kind != "state_update" || mutation.ProjectDir != root || mutation.Vars["retained"] != "unchanged" || mutation.Vars["custom"].(map[string]any)["answer"] != "42" {
		t.Fatalf("committed mutation=%+v", mutation)
	}
}
