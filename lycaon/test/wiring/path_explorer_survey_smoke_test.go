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

// TestPathExplorerChildSchemaIsCommandFree checks the survey tool boundary.
func TestPathExplorerChildSchemaIsCommandFree(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	child, err := h.SessionMgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
		AgentType: orchestration.ProfilePathExplorer,
		Prompt:    "Survey Go files under src/",
	})
	testutil.FailErr(t, "SpawnChild", err)
	prof, err := h.AgentRegistry.Get(orchestration.ProfilePathExplorer)
	testutil.FailErr(t, "agents.Get", err)
	policy := h.SessionMgr.Guards.Policy()
	var schema []string
	for _, meta := range policy.ListForPrompt(ctx, child, prof.ToolProfile) {
		schema = append(schema, meta.Name)
	}
	for _, name := range schema {
		if name == "command" {
			t.Fatalf("path-explorer schema must not include command: %v", schema)
		}
	}
	for _, want := range []string{"find", "grep", "read"} {
		if !containsString(schema, want) {
			t.Fatalf("path-explorer schema missing %q: %v", want, schema)
		}
	}
}

// TestPathExplorerSurveyWithFindAndGrepOnly exercises native survey tools.
func TestPathExplorerSurveyWithFindAndGrepOnly(t *testing.T) {
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
	_, err = h.WorkerQueue.Enqueue(ctx, task)
	testutil.FailErr(t, "enqueue worker", err)
	claimed, err := h.WorkerQueue.ClaimNext(ctx, worker.ClaimRequest{
		ProjectID: parent.ProjectID, ExecutionTarget: api.ExecutionTargetLocal, ClaimedBy: "path-explorer-test",
	})
	testutil.FailErr(t, "claim worker", err)
	exec := wiringWorkerExecutor(h)
	result, err := exec.Execute(ctx, *claimed, worker.RunContextForTask(*claimed, dir))
	testutil.FailErr(t, "worker Execute", err)
	if result.Status != string(api.WorkerStatusComplete) {
		stored, _ := h.WorkerQueue.Get(task.ID)
		var messages []api.Message
		if stored != nil {
			messages, _ = h.Store.GetWorkerJobMessages(ctx, stored.ChildSessionID, task.ID)
		}
		t.Fatalf("worker result = %+v want complete; task=%+v messages=%+v", result, stored, messages)
	}

	invoked := surveyMock.invokedTools()
	for _, name := range invoked {
		if name == "command" {
			t.Fatal("path-explorer survey must not invoke command")
		}
	}
	foundFind := false
	foundGrep := false
	for _, name := range invoked {
		switch name {
		case "find":
			foundFind = true
		case "grep":
			foundGrep = true
		}
	}
	if !foundFind || !foundGrep {
		t.Fatalf("expected find and grep tool calls; find=%v grep=%v invoked=%v", foundFind, foundGrep, invoked)
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
	if schemaHasTool(req.Tools, "find") && !schemaHasTool(req.Tools, "task") {
		n := m.workerTurn.Add(1)
		switch n {
		case 1:
			m.recordInvoke("find")
			return &modelcall.Completion{ToolCalls: []api.ToolCall{{
				ID: "f1", Name: "find",
				Args: map[string]any{"path": ".", "name_glob": "**/*.go"},
			}}}, nil
		case 2:
			m.recordInvoke("grep")
			return &modelcall.Completion{ToolCalls: []api.ToolCall{{
				ID: "g1", Name: "grep",
				Args: map[string]any{"pattern": "package main", "path": "."},
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
