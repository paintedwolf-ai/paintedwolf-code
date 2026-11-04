package wiring

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

// workerScriptMock plays one worker's tool calls in order and keeps each result.
type workerScriptMock struct {
	mu      sync.Mutex
	script  []api.ToolCall
	next    int
	results map[string]string
}

func (m *workerScriptMock) Complete(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if !schemaHasTool(req.Tools, "complete_leg") {
		return &modelcall.Completion{Content: "Done."}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, message := range req.Messages {
		if message.Role == api.MessageRoleTool && message.ToolResult != nil {
			m.results[message.ToolResult.ToolCallID] = message.ToolResult.Content
		}
	}
	if m.next >= len(m.script) {
		return &modelcall.Completion{Content: "Done."}, nil
	}
	call := m.script[m.next]
	m.next++
	return &modelcall.Completion{ToolCalls: []api.ToolCall{call}}, nil
}

func (m *workerScriptMock) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	completion, err := m.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: completion.Content, ToolCalls: completion.ToolCalls, Done: true}
	close(ch)
	return ch, nil
}

type workerHistoryResult struct {
	Mode     string `json:"mode"`
	Recorded bool   `json:"recorded"`
	Note     string `json:"note"`
	Branch   string `json:"branch"`
	Since    *struct {
		Ordinal int64 `json:"ordinal"`
	} `json:"since"`
	Files []struct {
		Path  string `json:"path"`
		Actor string `json:"actor"`
	} `json:"files"`
	BaseVersionID string `json:"base_version_id"`
	VersionID     string `json:"version_id"`
	Diff          string `json:"diff"`
}

func decodeWorkerHistory(t *testing.T, results map[string]string, callID string) workerHistoryResult {
	t.Helper()
	raw, ok := results[callID]
	if !ok {
		t.Fatalf("no result for %s in %v", callID, results)
	}
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end < start {
		t.Fatalf("%s result = %q, want a JSON body", callID, raw)
	}
	var result workerHistoryResult
	testutil.FailErr(t, "decode "+callID, json.Unmarshal([]byte(raw[start:end+1]), &result))
	return result
}

func historyCall(id string, args map[string]any) api.ToolCall {
	return api.ToolCall{ID: id, Name: "source_history", Args: args}
}

func commandCall(id, source string) api.ToolCall {
	return api.ToolCall{ID: id, Name: "command", Args: map[string]any{"command": source}}
}

// runWorkerScript drives a worker through the queue and the real executor: no
// fixture step creates its session, its checkpoint or its branch.
func runWorkerScript(t *testing.T, agentType string, scope *api.TaskScope, script ...api.ToolCall) (string, map[string]string) {
	t.Helper()
	mock := &workerScriptMock{script: script, results: map[string]string{}}
	h := BuildForTest(t, WithLLMClient(mock))
	ctx := context.Background()
	dir := t.TempDir()
	writeTestProjectApprovalsAllowWrite(t, dir)
	testutil.FailErr(t, "seed main.go", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create parent", err)

	ledger := sourceledger.New(h.DB, t.TempDir())
	testutil.FailErr(t, "record a coordinator change", ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: parent.WorkspaceRootID, Path: "coordinator.go"},
		ProjectID:      parent.ProjectID, Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
		SessionID: parent.ID, Turn: 1, OperationID: "coordinator-change", After: []byte("package coordinator\n"),
	}))

	task := api.WorkerTask{
		ParentSessionID: parent.ID, Prompt: "Change main.go.", Brief: "fixture", AgentType: agentType, Scope: scope,
	}
	testutil.FailErr(t, "worker defaults", worker.ApplyEnqueueDefaults(
		&task, project.ProjectScope{ProjectID: parent.ProjectID, WorkspacePath: dir}, worker.DefaultWorkersConfig(),
	))
	jobID, err := h.Delegations.Queue.Enqueue(ctx, task)
	testutil.FailErr(t, "enqueue worker", err)
	claimed, err := h.Delegations.Queue.ClaimNext(ctx, worker.ClaimRequest{
		ProjectID: parent.ProjectID, ExecutionTarget: api.ExecutionTargetLocal, ClaimedBy: "source-history-test",
	})
	testutil.FailErr(t, "claim worker", err)
	if _, err := wiringWorkerExecutor(h).Execute(ctx, *claimed, worker.RunContextForTask(*claimed, dir)); err != nil {
		testutil.FailErr(t, "worker Execute", err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	return jobID, mock.results
}

func writeScope() *api.TaskScope {
	return &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"."}}
}

func TestSourceHistoryACOV04WriteWorkerWindowIsItsWholeBranch(t *testing.T) {
	jobID, results := runWorkerScript(t, orchestration.ProfileImplementer, writeScope(),
		api.ToolCall{ID: "load", Name: "request_tools", Args: map[string]any{"need": "source_history"}},
		commandCall("write", `printf 'package main\n// edited\n' > main.go`),
		historyCall("overview", map[string]any{}),
	)
	overview := decodeWorkerHistory(t, results, "overview")
	if !overview.Recorded || overview.Since == nil || overview.Since.Ordinal != 0 || overview.Branch != jobID {
		t.Fatalf("overview = %+v, want recorded from ordinal 0 on branch %s", overview, jobID)
	}
	if len(overview.Files) != 1 || overview.Files[0].Path != "main.go" || overview.Files[0].Actor != "you" {
		t.Fatalf("rows = %+v, want the worker's own write and no coordinator change", overview.Files)
	}
}

func TestSourceHistoryACOV15WorkerWithNothingToShow(t *testing.T) {
	t.Run("write worker before its first write", func(t *testing.T) {
		jobID, results := runWorkerScript(t, orchestration.ProfileImplementer, writeScope(),
			api.ToolCall{ID: "load", Name: "request_tools", Args: map[string]any{"need": "source_history"}},
			historyCall("overview", map[string]any{}),
		)
		overview := decodeWorkerHistory(t, results, "overview")
		if !overview.Recorded || overview.Branch != jobID || len(overview.Files) != 0 || overview.Note == "" {
			t.Fatalf("overview = %+v, want recorded, no rows and a note on branch %s", overview, jobID)
		}
	})
	t.Run("read-scoped worker on the trunk", func(t *testing.T) {
		_, results := runWorkerScript(t, orchestration.ProfileRepoResearcher, &api.TaskScope{Mode: api.TaskScopeModeRead},
			historyCall("overview", map[string]any{}),
			historyCall("diff", map[string]any{"path": "main.go", "mode": "diff"}),
		)
		for _, call := range []string{"overview", "diff"} {
			result := decodeWorkerHistory(t, results, call)
			if result.Recorded || len(result.Files) != 0 || result.Note == "" || result.Since != nil {
				t.Fatalf("%s = %+v, want an unknown window with a note", call, result)
			}
		}
	})
}

func TestSourceHistoryACDF14WriteWorkerDiffStartsAtTheBranchPreimage(t *testing.T) {
	_, results := runWorkerScript(t, orchestration.ProfileImplementer, writeScope(),
		api.ToolCall{ID: "load", Name: "request_tools", Args: map[string]any{"need": "source_history"}},
		commandCall("write", `printf 'package main\n// edited\n' > main.go`),
		historyCall("diff", map[string]any{"path": "main.go", "mode": "diff"}),
	)
	diff := decodeWorkerHistory(t, results, "diff")
	if !diff.Recorded || diff.BaseVersionID == "" || diff.VersionID == "" || diff.BaseVersionID == diff.VersionID {
		t.Fatalf("diff = %+v, want the branch pre-image against head", diff)
	}
	var added []string
	for line := range strings.SplitSeq(diff.Diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added = append(added, line)
		}
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			t.Fatalf("diff removed %q from the pre-image: %q", line, diff.Diff)
		}
	}
	if len(added) != 1 || added[0] != "+ // edited" {
		t.Fatalf("added lines = %q in %q, want the worker's one line", added, diff.Diff)
	}
}
