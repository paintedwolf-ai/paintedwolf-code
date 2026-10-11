package statetools

import (
	"context"
	"errors"
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
	failure  error
}

func (c *updateCommands) ReplayCommandOperation(context.Context, string, string, string) (*api.WorkflowRun, bool, error) {
	return &api.WorkflowRun{ID: "run"}, c.replayed, nil
}
func (c *updateCommands) CommitCommand(_ context.Context, _ *api.WorkflowRun, m runstate.CommandMutation) error {
	if c.failure != nil {
		return c.failure
	}
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
		t.Fatalf("RegisterStateTools failed: %v", err)
	}
	ctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session", ToolCallID: "operation"}, Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}}}
	args := map[string]any{"path": "custom.answer", "value": "42"}
	first, err := reg.Run(t.Context(), "state_update", args, ctx)
	if err != nil {
		t.Fatalf("operation failed: %v", err)
	}
	replay, err := reg.Run(t.Context(), "state_update", args, ctx)
	if err != nil {
		t.Fatalf("operation failed: %v", err)
	}
	if first != replay || len(commands.commits) != 1 {
		t.Fatalf("first=%q replay=%q commits=%v", first, replay, commands.commits)
	}
	mutation := commands.commits[0]
	if mutation.OperationID != "operation" || mutation.Kind != "state_update" || mutation.ProjectDir != root || mutation.Vars["retained"] != "unchanged" || mutation.Vars["custom"].(map[string]any)["answer"] != "42" {
		t.Fatalf("committed mutation=%+v", mutation)
	}
}

func TestStateUpdateRefusesFailedTransactionWithoutPublishingReceipt(t *testing.T) {
	root := t.TempDir()
	failure := errors.New("transaction refused")
	commands := &updateCommands{failure: failure}
	reg := tools.NewDefaultRegistry()
	runs := queryRuns{active: &api.WorkflowRun{ID: "run", SessionID: "session", Revision: 4}, vars: map[string]any{"retained": "unchanged"}}
	deps := StateToolDeps{Runs: runs, Sessions: querySessions{root}, Vars: runstate.NewVariables(nil, nil, nil), Journal: &runstate.Journal{Commands: commands, Directories: updateDirectories{root}}}
	if err := RegisterStateTools(reg, deps); err != nil {
		t.Fatalf("register state tools: %v", err)
	}
	_, err := reg.Run(t.Context(), "state_update", map[string]any{"path": "custom.answer", "value": "42"}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session", ToolCallID: "operation"}, Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}}})
	if !errors.Is(err, failure) || commands.replayed || len(commands.commits) != 0 {
		t.Fatalf("failed transaction published receipt=%+v err=%v", commands, err)
	}
}
