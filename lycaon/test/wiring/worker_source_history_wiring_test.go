package wiring

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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
	Files         []workerHistoryRow `json:"files"`
	BaseVersionID string             `json:"base_version_id"`
	VersionID     string             `json:"version_id"`
	State         string             `json:"state"`
	Unrecorded    bool               `json:"unrecorded"`
	Diff          string             `json:"diff"`
}

type workerHistoryRow struct {
	Path       string `json:"path"`
	Op         string `json:"op"`
	Actor      string `json:"actor"`
	At         string `json:"at"`
	VersionID  string `json:"version_id"`
	Unrecorded bool   `json:"unrecorded"`
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
	jobID, results, _ := runWorkerScriptOnHarness(t, agentType, scope, script...)
	return jobID, results
}

func runWorkerScriptOnHarness(t *testing.T, agentType string, scope *api.TaskScope, script ...api.ToolCall) (string, map[string]string, *Harness) {
	t.Helper()
	mock := &workerScriptMock{script: script, results: map[string]string{}}
	h := BuildForTest(t, WithLLMClient(mock))
	ctx := context.Background()
	dir := t.TempDir()
	writeTestProjectApprovalsAllowWrite(t, dir)
	testutil.FailErr(t, "seed main.go", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	testutil.FailErr(t, "seed obsolete.go", os.WriteFile(filepath.Join(dir, "obsolete.go"), []byte("package obsolete\n"), 0o644))
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
	return jobID, mock.results, h
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

// generatorCall runs an external program, which leaves no ledger row on a worker
// branch: it creates gen.txt, rewrites main.go and removes obsolete.go.
func generatorCall(id string) api.ToolCall {
	return commandCall(id, `external -- /bin/sh -c 'printf "generated\n" > gen.txt; printf "package main\n// generated\n" > main.go; rm obsolete.go'`)
}

func unrecordedRow(t *testing.T, rows []workerHistoryRow, path, op string) {
	t.Helper()
	for _, row := range rows {
		if row.Path != path {
			continue
		}
		if !row.Unrecorded || row.Op != op || row.Actor != "" || row.At != "" || row.VersionID != "" {
			t.Fatalf("row %+v, want unrecorded %s with no actor, time or version", row, op)
		}
		return
	}
	t.Fatalf("rows = %+v, missing %s", rows, path)
}

func TestSourceHistoryACOV18ExternalProgramChangesAppearUnrecorded(t *testing.T) {
	jobID, results := runWorkerScript(t, orchestration.ProfileImplementer, writeScope(),
		api.ToolCall{ID: "load", Name: "request_tools", Args: map[string]any{"need": "source_history"}},
		generatorCall("generate"),
		historyCall("overview", map[string]any{}),
	)
	overview := decodeWorkerHistory(t, results, "overview")
	if !overview.Recorded || overview.Branch != jobID || len(overview.Files) != 3 {
		t.Fatalf("overview = %+v, want three rows on branch %s", overview, jobID)
	}
	unrecordedRow(t, overview.Files, "gen.txt", "create")
	unrecordedRow(t, overview.Files, "main.go", "write")
	unrecordedRow(t, overview.Files, "obsolete.go", "delete")
}

func TestSourceHistoryACOV20PathWithALedgerRowIsNotDuplicated(t *testing.T) {
	_, results := runWorkerScript(t, orchestration.ProfileImplementer, writeScope(),
		api.ToolCall{ID: "load", Name: "request_tools", Args: map[string]any{"need": "source_history"}},
		commandCall("write", `printf 'package main\n// edited\n' > main.go`),
		generatorCall("generate"),
		historyCall("overview", map[string]any{}),
	)
	overview := decodeWorkerHistory(t, results, "overview")
	seen := map[string]int{}
	for _, row := range overview.Files {
		seen[row.Path]++
		if row.Path == "main.go" && (row.Unrecorded || row.Actor != "you" || row.VersionID == "") {
			t.Fatalf("main.go row = %+v, want the ledger's own row", row)
		}
	}
	if seen["main.go"] != 1 || seen["gen.txt"] != 1 || seen["obsolete.go"] != 1 {
		t.Fatalf("rows = %+v, want one row per path", overview.Files)
	}
}

func TestSourceHistoryACDF15DefaultDiffOnAnUnrecordedFile(t *testing.T) {
	_, results := runWorkerScript(t, orchestration.ProfileImplementer, writeScope(),
		api.ToolCall{ID: "load", Name: "request_tools", Args: map[string]any{"need": "source_history"}},
		generatorCall("generate"),
		historyCall("created", map[string]any{"path": "gen.txt", "mode": "diff"}),
		historyCall("modified", map[string]any{"path": "main.go", "mode": "diff"}),
		historyCall("deleted", map[string]any{"path": "obsolete.go", "mode": "diff"}),
	)
	for _, c := range []struct {
		call, state string
		added, gone []string
	}{
		{call: "created", added: []string{"+ generated"}},
		{call: "modified", added: []string{"+ // generated"}},
		{call: "deleted", state: "absent", gone: []string{"- package obsolete"}},
	} {
		diff := decodeWorkerHistory(t, results, c.call)
		if diff.Recorded || !diff.Unrecorded || diff.BaseVersionID != "" || diff.VersionID != "" || diff.State != c.state {
			t.Fatalf("%s = %+v, want an unrecorded diff with no versions and state %q", c.call, diff, c.state)
		}
		var added, gone []string
		for line := range strings.SplitSeq(diff.Diff, "\n") {
			switch {
			case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			case strings.HasPrefix(line, "+"):
				added = append(added, line)
			case strings.HasPrefix(line, "-"):
				gone = append(gone, line)
			}
		}
		if !slices.Equal(added, c.added) || !slices.Equal(gone, c.gone) {
			t.Fatalf("%s added %q removed %q in %q, want %q and %q", c.call, added, gone, diff.Diff, c.added, c.gone)
		}
	}
}

func TestSourceHistoryACDF16DiffShowsTheGeneratorsRewriteOfARecordedFile(t *testing.T) {
	_, results := runWorkerScript(t, orchestration.ProfileImplementer, writeScope(),
		api.ToolCall{ID: "load", Name: "request_tools", Args: map[string]any{"need": "source_history"}},
		commandCall("write", `printf 'package main\n// edited\n' > main.go`),
		generatorCall("generate"),
		historyCall("overview", map[string]any{}),
		historyCall("diff", map[string]any{"path": "main.go", "mode": "diff"}),
	)
	diff := decodeWorkerHistory(t, results, "diff")
	if !diff.Recorded || diff.Unrecorded || diff.BaseVersionID == "" || diff.VersionID != "" || diff.Note == "" {
		t.Fatalf("diff = %+v, want the recorded baseline, no live version and a note", diff)
	}
	if !strings.Contains(diff.Diff, "// generated") || strings.Contains(diff.Diff, "// edited") {
		t.Fatalf("diff = %q, want the generator's line and not the replaced native one", diff.Diff)
	}
	overview := decodeWorkerHistory(t, results, "overview")
	for _, row := range overview.Files {
		if row.Path == "main.go" && (row.Unrecorded || row.Actor != "you" || row.VersionID == "") {
			t.Fatalf("main.go row = %+v, want its ledger facts", row)
		}
	}
}

// A FIFO has no writer, so a blocking open in the diff would never return.
func TestSourceHistoryACDF17SpecialFileAnswersWithANote(t *testing.T) {
	_, results := runWorkerScript(t, orchestration.ProfileImplementer, writeScope(),
		api.ToolCall{ID: "load", Name: "request_tools", Args: map[string]any{"need": "source_history"}},
		commandCall("fifo", `external -- /bin/sh -c 'mkfifo pipe'`),
		historyCall("overview", map[string]any{}),
		historyCall("diff", map[string]any{"path": "pipe", "mode": "diff"}),
	)
	unrecordedRow(t, decodeWorkerHistory(t, results, "overview").Files, "pipe", "create")
	diff := decodeWorkerHistory(t, results, "diff")
	if !diff.Unrecorded || diff.Diff != "" || diff.Note == "" {
		t.Fatalf("diff = %+v, want an unrecorded answer with a note and no text", diff)
	}
}

// Through the real branch mapping and evidence store, a credential file's diff
// marks the worker's session as exposed.
func TestSourceHistoryACDF19CredentialDiffMarksTheWorkersSession(t *testing.T) {
	jobID, results, h := runWorkerScriptOnHarness(t, orchestration.ProfileImplementer, writeScope(),
		api.ToolCall{ID: "load", Name: "request_tools", Args: map[string]any{"need": "source_history"}},
		commandCall("generate", `external -- /bin/sh -c 'printf "TOKEN=generated-value\n" > .env'`),
		historyCall("diff", map[string]any{"path": ".env", "mode": "diff"}),
	)
	diff := decodeWorkerHistory(t, results, "diff")
	if !strings.Contains(diff.Diff, "TOKEN") {
		t.Fatalf("diff = %+v, want the file's text", diff)
	}
	job, ok := h.Delegations.Queue.Get(jobID)
	if !ok || job.ChildSessionID == "" {
		t.Fatalf("job = %+v, want its child session", job)
	}
	exposed, err := h.Store.SessionSecretExposure(context.Background(), job.ChildSessionID)
	testutil.FailErr(t, "secret exposure", err)
	if !exposed {
		t.Fatal("the worker's session is not marked as exposed")
	}
}

func TestSourceHistoryACDF20NativeCreateThenProgramDeleteIsAbsent(t *testing.T) {
	_, results := runWorkerScript(t, orchestration.ProfileImplementer, writeScope(),
		api.ToolCall{ID: "load", Name: "request_tools", Args: map[string]any{"need": "source_history"}},
		commandCall("write", `printf 'scratch\n' > tmp.txt`),
		commandCall("remove", `external -- /bin/sh -c 'rm tmp.txt'`),
		historyCall("diff", map[string]any{"path": "tmp.txt", "mode": "diff"}),
	)
	diff := decodeWorkerHistory(t, results, "diff")
	if !diff.Recorded || diff.Unrecorded || diff.State != "absent" || diff.Diff != "" || diff.Note == "" {
		t.Fatalf("diff = %+v, want a recorded absent answer with a note and no text", diff)
	}
}
