package statetools

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

type queryRuns struct {
	runstate.RunsRepository
	active *api.WorkflowRun
	vars   map[string]any
	err    error
}

func (r queryRuns) ActiveBySession(context.Context, string) (*api.WorkflowRun, error) {
	return r.active, r.err
}
func (r queryRuns) GetScaffoldVars(context.Context, string) (map[string]any, error) {
	return r.vars, r.err
}

type querySessions struct{ root string }

func (s querySessions) Get(context.Context, string) (*api.Session, error) {
	return &api.Session{WorkspacePath: s.root}, nil
}

func TestStateQueryReadsActiveScaffoldAndRefusesMissingState(t *testing.T) {
	root := t.TempDir()
	tctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}, Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}}}
	for _, tc := range []struct {
		name    string
		runs    queryRuns
		args    map[string]any
		want    string
		refused bool
	}{
		{name: "idle", want: "{}"},
		{name: "nested", runs: queryRuns{active: &api.WorkflowRun{ID: "run"}, vars: map[string]any{"custom": map[string]any{"answer": "retained"}}}, args: map[string]any{"path": "custom.answer"}, want: "retained"},
		{name: "whole state", runs: queryRuns{active: &api.WorkflowRun{ID: "run"}, vars: map[string]any{"answer": "retained"}}, want: "retained"},
		{name: "absent path", runs: queryRuns{active: &api.WorkflowRun{ID: "run"}, vars: map[string]any{}}, args: map[string]any{"path": "missing"}, refused: true},
		{name: "store failure", runs: queryRuns{err: errors.New("store unavailable")}, refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := tools.NewDefaultRegistry()
			if err := RegisterStateTools(reg, StateToolDeps{Runs: tc.runs, Sessions: querySessions{root}}); err != nil {
				t.Fatalf("RegisterStateTools failed: %v", err)
			}
			out, err := reg.Run(t.Context(), "state_query", tc.args, tctx)
			if (err != nil) != tc.refused {
				t.Fatalf("output=%q err=%v", out, err)
			}
			if err == nil {
				var decoded any
				if err = json.Unmarshal([]byte(out), &decoded); err != nil {
					t.Fatalf("unmarshal JSON document: %v", err)
				}
				if tc.name == "idle" && out != tc.want {
					t.Fatalf("idle=%q", out)
				}
				if tc.name == "whole state" && decoded.(map[string]any)["vars"].(map[string]any)["answer"] != tc.want {
					t.Fatalf("retained scaffold=%v", decoded)
				}
				if tc.name == "nested" {
					if decoded.(map[string]any)["value"] != tc.want {
						t.Fatalf("query=%v", decoded)
					}
				}
			}
		})
	}
	reg := tools.NewDefaultRegistry()
	if err := RegisterStateTools(reg, StateToolDeps{Runs: queryRuns{}, Sessions: querySessions{root}}); err != nil {
		t.Fatalf("RegisterStateTools failed: %v", err)
	}
	for _, name := range []string{"state_close", "state_start", "state_update"} {
		if _, err := reg.Run(t.Context(), name, nil, tctx); err == nil {
			t.Fatalf("%s accepted missing active state/arguments", name)
		}
	}
	for _, path := range []string{"review_loop.current", "gates.decision", "hitl_consulted:phase"} {
		_, err := reg.Run(t.Context(), "state_update", map[string]any{"path": path, "value": true}, tctx)
		var rejection *toolrejection.ToolReject
		if !errors.As(err, &rejection) || rejection.Code != "TOOL_ARGS_INVALID" || rejection.Data["reason"] != "host_managed_workflow_state" || rejection.Data["path"] != path {
			t.Fatalf("protected state %s refusal=%v", path, err)
		}
	}
}
