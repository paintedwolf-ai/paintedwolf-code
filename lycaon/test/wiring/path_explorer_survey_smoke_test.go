package wiring

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

// TestPathExplorerChildSchemaUsesNativeCommandSurvey checks the survey tool boundary.
func TestPathExplorerChildSchemaUsesNativeCommandSurvey(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	child, err := h.Sessions.Manager.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
		AgentType: orchestration.ProfilePathExplorer,
		Prompt:    "Survey Go files under src/",
	})
	testutil.FailErr(t, "SpawnChild", err)
	prof, err := h.AgentRegistry.Get(orchestration.ProfilePathExplorer)
	testutil.FailErr(t, "agents.Get", err)
	policy := h.Sessions.Manager.Coordinator.Guards.Policy()
	var schema []string
	for _, meta := range policy.ListForPrompt(ctx, child, prof.ToolProfile) {
		schema = append(schema, meta.Name)
	}
	for _, forbidden := range []string{"edit", "replace_lines", "code_rewrite", "restore_version", "chmod", "chown", "read", "find", "grep"} {
		if containsString(schema, forbidden) {
			t.Fatalf("path-explorer schema must not include unavailable tool %q: %v", forbidden, schema)
		}
	}
	for _, want := range []string{"command", "survey_repo", "complete_leg"} {
		if !containsString(schema, want) {
			t.Fatalf("path-explorer schema missing %q: %v", want, schema)
		}
	}
}

// TestPathExplorerSurveyUsesNativeFindAndGrep exercises native survey tools.
func TestPathExplorerSurveyUsesNativeFindAndGrep(t *testing.T) {
	surveyMock := &pathExplorerSurveyMock{}
	h := BuildForTest(t, WithLLMClient(surveyMock))

	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		testutil.FailErr(t, "write main.go", err)
	}
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)

	task := api.WorkerTask{
		ID:              "job-path-explorer",
		ParentSessionID: parent.ID,
		Prompt:          "Survey Go files and locate package declarations.",
		Brief:           "fixture",
		AgentType:       orchestration.ProfilePathExplorer,
	}
	testutil.FailErr(t, "worker defaults", worker.ApplyEnqueueDefaults(
		&task, project.ProjectScope{ProjectID: parent.ProjectID, WorkspacePath: dir}, worker.DefaultWorkersConfig(),
	))
	_, err = h.Delegations.Queue.Enqueue(ctx, task)
	testutil.FailErr(t, "enqueue worker", err)
	claimed, err := h.Delegations.Queue.ClaimNext(ctx, worker.ClaimRequest{
		ProjectID: parent.ProjectID, ExecutionTarget: api.ExecutionTargetLocal, ClaimedBy: "path-explorer-test",
	})
	testutil.FailErr(t, "claim worker", err)
	exec := wiringWorkerExecutor(h)
	result, err := exec.Execute(ctx, *claimed, worker.RunContextForTask(*claimed, dir))
	testutil.FailErr(t, "worker Execute", err)
	if result.Status != string(api.WorkerStatusComplete) {
		t.Fatalf("worker status=%s hint=%s report=%+v", result.Status, result.HintCode, result.CompletionReport)
	}

	invoked := surveyMock.invokedTools()
	if len(invoked) != 3 || invoked[0] != "command" || invoked[1] != "command" || invoked[2] != "complete_leg" {
		t.Fatalf("survey calls=%v", invoked)
	}
	stored, ok := h.Delegations.Queue.Get(task.ID)
	if !ok || stored == nil || stored.ChildSessionID == "" {
		t.Fatal("worker child session missing")
	}
	for _, operation := range []string{"find", "grep"} {
		var count int
		testutil.FailErr(t, "read native survey receipt", h.DB.QueryRowContext(ctx, `SELECT count(*) FROM invocation_program_executions e JOIN invocation_programs p ON p.id=e.program_id JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE r.session_id=? AND e.operation_id=? AND e.state='completed' AND e.owner_started=1`, stored.ChildSessionID, operation).Scan(&count))
		if count != 1 {
			t.Fatalf("native %s completed receipts=%d", operation, count)
		}
	}

}

type pathExplorerSurveyMock struct {
	workerTurn atomic.Int32
	mu         sync.Mutex
	invoked    []string
}

func (m *pathExplorerSurveyMock) recordInvoke(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invoked = append(m.invoked, name)
}

func (m *pathExplorerSurveyMock) invokedTools() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]string(nil), m.invoked...)
	return out
}

func (m *pathExplorerSurveyMock) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if schemaHasTool(req.Tools, "command") && !schemaHasTool(req.Tools, "task") {
		n := m.workerTurn.Add(1)
		switch n {
		case 1:
			m.recordInvoke("command")
			return &modelcall.Completion{ToolCalls: []api.ToolCall{{
				ID: "f1", Name: "command",
				Args: map[string]any{"command": "find . -name '*.go' -type f"},
			}}}, nil
		case 2:
			m.recordInvoke("command")
			return &modelcall.Completion{ToolCalls: []api.ToolCall{{
				ID: "g1", Name: "command",
				Args: map[string]any{"command": "grep -n 'package main' main.go"},
			}}}, nil
		case 3:
			m.recordInvoke("complete_leg")
			return &modelcall.Completion{ToolCalls: []api.ToolCall{{
				ID: "finish-survey", Name: "complete_leg", Args: map[string]any{
					"leg_status": "complete", "brief": "Located one Go package entry point.",
					"findings":       []any{map[string]any{"path": "main.go", "line": 1, "excerpt": "package main"}},
					"objectives_met": []any{"Located the package entry point."},
				},
			}}}, nil
		default:
			return &modelcall.Completion{Content: "Done."}, nil
		}
	}
	return &modelcall.Completion{Content: "Done."}, nil
}

func (m *pathExplorerSurveyMock) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	completion, err := m.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan modelcall.StreamChunk, 1)
	if len(completion.ToolCalls) > 0 {
		ch <- modelcall.StreamChunk{ToolCalls: completion.ToolCalls, Done: true}
	} else {
		ch <- modelcall.StreamChunk{Content: completion.Content, Done: true}
	}
	close(ch)
	return ch, nil
}

func schemaHasTool(metas []tools.ToolMeta, name string) bool {
	for _, meta := range metas {
		if meta.Name == name {
			return true
		}
	}
	return false
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
