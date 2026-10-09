package promptloop_test

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTaskArgumentValidationPrecedesScopeGuard(t *testing.T) {
	valid := map[string]any{
		"agent_type": "implementer",
		"brief":      map[string]any{"goal": "Implement the engine", "done_when": []any{"Tests pass"}},
		"scope":      map[string]any{"mode": "write"},
	}
	cases := []struct {
		name  string
		call  api.ToolCall
		code  string
		field string
		guard bool
		owner bool
	}{
		{name: "scope swallowed by string brief", call: api.ToolCall{Args: map[string]any{
			"agent_type": "implementer",
			"brief":      `{"goal":"Implement the engine","done_when":["Tests pass"]}, "files":["Sources"], "scope":{"mode":"write"}}`,
		}}, code: "TOOL_ARGS_INVALID", field: "brief"},
		{name: "malformed transport", call: api.ToolCall{ArgsMalformed: true}, code: "TOOL_ARGS_MALFORMED"},
		{name: "truncated transport", call: api.ToolCall{ArgsTruncated: true}, code: "TOOL_ARGS_TRUNCATED"},
		{name: "valid shape still needs write scope", call: api.ToolCall{Args: taskCallArgs("implementer", "Implement the engine")}, code: "TASK_SCOPE_WRITE_REQUIRED", guard: true},
		{name: "corrected dispatch", call: api.ToolCall{Args: valid}, guard: true, owner: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.call.ID, tc.call.Name = "dispatch", "task"
			settlement, guarded, ran := runTaskArgumentCall(t, tc.call)
			if guarded != tc.guard || ran != tc.owner {
				t.Fatalf("guard=%v owner=%v; want guard=%v owner=%v", guarded, ran, tc.guard, tc.owner)
			}
			if tc.code == "" {
				if settlement.Status != api.InvocationStatusCompleted {
					t.Fatalf("corrected dispatch status = %s", settlement.Status)
				}
				return
			}
			if settlement.Status != api.InvocationStatusRejected || settlement.Invoked || settlement.Failure == nil || settlement.Failure.Code != tc.code {
				t.Fatalf("rejection receipt = %+v; want %s before owner invocation", settlement, tc.code)
			}
			if tc.field != "" {
				// The brief text closed, then carried the call's files and scope.
				details := settlement.Failure.Details
				trailing, _ := details["json_trailing_members"].([]string)
				if details["field"] != tc.field || details["json_malformed"] != true || !slices.Equal(trailing, []string{"files", "scope"}) {
					t.Fatalf("diagnostic did not name the members after the early close: %v", details)
				}
			}
		})
	}
}

func runTaskArgumentCall(t *testing.T, call api.ToolCall) (invocation.Settlement, bool, bool) {
	t.Helper()
	guarded, ran := false, false
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "load task schema", err)
	meta, ok := cfg.ToolMeta("task")
	if !ok {
		t.Fatal("task schema missing")
	}
	reg := tools.NewStubRegistry()
	testutil.FailErr(t, "register task", reg.Register("task", func(_ context.Context, _ map[string]any, tc tools.ToolContext) (string, error) {
		ran = true
		tc.Out.Dispatch = &api.WorkerDispatch{WorkerID: "engine"}
		return "queued", nil
	}))
	def, _ := reg.Definition("task")
	def.Meta.ArgsSchema = meta.ArgsSchema
	testutil.FailErr(t, "publish task schema", reg.RegisterDefinition(def))
	mem := store.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	rec := newRecordingRecorder()
	deps := promptloop.StoreDeps(mem)
	deps.Context.LoadedTools = workersLoaded
	deps.Context.Tools, deps.Tools.Invocations = reg, rec
	deps.Context.Limits = loopTestLimits(2)
	deps.Model.LLM = &sequentialLLMClient{completions: []*modelcall.Completion{{ToolCalls: []api.ToolCall{call}}, {Content: "Done"}}}
	deps.Tools.BeforeToolRun = func(_ context.Context, _ *api.Session, _ []api.Message, _, _ string, args map[string]any) (string, bool, error) {
		guarded = true
		scope, scopeErr := api.TaskScopeFromArgs(args)
		if scopeErr != nil {
			return "", false, scopeErr
		}
		if !scope.IsWrite() {
			return "", false, guidance.NewRefusal("TASK_SCOPE_WRITE_REQUIRED", "write scope required")
		}
		return "", false, nil
	}
	_, err = promptloop.NewPromptLoopForTest(deps).Run(t.Context(), promptloop.PromptRunInput{
		SessionID: sess.ID, Session: sess, History: userHistory("Build an app"), ProfileID: "coordinator",
		ToolCtx: tools.ToolContext{SessionID: sess.ID},
	})
	testutil.FailErr(t, "run dispatch", err)
	_, settlement := rec.only(t)
	return settlement, guarded, ran
}
