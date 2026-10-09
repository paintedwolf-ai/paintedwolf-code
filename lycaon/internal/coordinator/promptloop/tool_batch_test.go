package promptloop_test

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"context"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func commitEvidenceFromStore(store *store.Memory) func(context.Context, string, *api.Session, string, map[string]any, string, string) (string, string, error) {
	return func(ctx context.Context, sessionID string, sess *api.Session, toolName string, args map[string]any, content, artifactID string) (string, string, error) {
		if sess == nil {
			return "", content, nil
		}
		return store.CommitVisualEvidenceToolResult(ctx, sessionID, sess.WorkspacePath, toolName, args, content, artifactID)
	}
}

func loopTestLimits(maxIter int) func(context.Context, *api.Session) settings.SessionLimits {
	return func(context.Context, *api.Session) settings.SessionLimits {
		lim := settings.DefaultSessionLimits()
		if maxIter > 0 {
			lim.MaxIterations = maxIter
		}
		return lim
	}
}

func mockToolThenDone(userPrompt string, toolCalls []llm.MockToolCall) []llm.MockResponseEntry {
	return []llm.MockResponseEntry{{
		Pattern:      userPrompt,
		ToolCalls:    toolCalls,
		FollowUpText: "done",
	}}
}

func TestLoopRunsConcurrentTools(t *testing.T) {
	var concurrent int32
	var peak int32
	reg := tools.NewStubRegistry()
	_ = reg.Register("read", func(ctx context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
		cur := atomic.AddInt32(&concurrent, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if cur > old && atomic.CompareAndSwapInt32(&peak, old, cur) {
				break
			}
			if cur <= old {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		atomic.AddInt32(&concurrent, -1)
		path, _ := args["path"].(string)
		return "contents of " + path, nil
	})

	client := llm.NewMockProvider(&llm.MockConfig{Responses: mockToolThenDone("survey", []llm.MockToolCall{
		{ID: "tc1", Name: "read", Args: map[string]any{"path": "a.go"}},
		{ID: "tc2", Name: "read", Args: map[string]any{"path": "b.go"}},
	})})
	store := store.NewMemory()
	deps := promptloop.StoreDeps(store)
	deps.LoadedTools = workersLoaded
	deps.Limits = loopTestLimits(2)
	deps.LLM = client
	deps.Tools = reg
	deps.Policy = &recordingToolPolicy{}
	deps.CoordinatorFrame = investigateCoordinatorContext()
	deps.CommitEvidenceToolResult = commitEvidenceFromStore(store)
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("survey"),
		ProfileID: "coordinator",
	})
	testutil.FailErr(t, "loop.Run", err)
	if atomic.LoadInt32(&peak) < 2 {
		t.Fatalf("peak concurrent reads = %d want >= 2", peak)
	}
}

func TestLoopCoordinatorReadsGetHandlesAcrossSurfaces(t *testing.T) {
	for _, surface := range []string{toolcontract.SurfaceImplementInvestigate, "decision_adjudicate", "recon_reconcile"} {
		t.Run(surface, func(t *testing.T) {
			reg := tools.NewStubRegistry()
			_ = reg.Register("read", func(_ context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
				path, _ := args["path"].(string)
				return "contents of " + path, nil
			})
			client := llm.NewMockProvider(&llm.MockConfig{Responses: mockToolThenDone("review the code", []llm.MockToolCall{
				{ID: "tc1", Name: "read", Args: map[string]any{"path": "a.go"}},
				{ID: "tc2", Name: "read", Args: map[string]any{"path": "b.go"}},
			})})
			store := store.NewMemory()
			deps := promptloop.StoreDeps(store)
			deps.LoadedTools = workersLoaded
			// The final iteration is prose-only.
			deps.Limits = loopTestLimits(2)
			deps.LLM = client
			deps.Tools = reg
			deps.Policy = &recordingToolPolicy{}
			deps.CoordinatorFrame = staticCoordinatorContext{run: api.CoordinatorRunContext{WorkflowID: "release", CurrentPhase: "record", PhaseCoordinatorSurface: surface}}
			deps.CommitEvidenceToolResult = commitEvidenceFromStore(store)
			loop := promptloop.NewPromptLoopForTest(deps)
			ctx := context.Background()
			sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			_, err = loop.Run(ctx, promptloop.PromptRunInput{
				SessionID:  sess.ID,
				Session:    sess,
				History:    userHistory("review the code"),
				UserPrompt: "review the code",
				ProfileID:  "coordinator",
				ToolCtx:    tools.ToolContext{TurnSurfaceID: surface},
			})
			testutil.FailErr(t, "loop.Run", err)

			msgs, err := store.GetMessages(ctx, sess.ID)
			testutil.FailErr(t, "get messages", err)
			got := map[string]string{}
			for _, m := range msgs {
				if m.Role != api.MessageRoleTool || m.ToolResult == nil {
					continue
				}
				if nl := strings.IndexByte(m.Content, '\n'); nl > 0 && strings.HasPrefix(m.Content, "[read#") {
					path, _ := m.ToolResult.ToolArgs["path"].(string)
					got[path] = m.Content[:nl]
				}
			}
			want := map[string]string{"a.go": "[read#1]", "b.go": "[read#2]"}
			if got["a.go"] != want["a.go"] || got["b.go"] != want["b.go"] {
				t.Fatalf("coordinator reads tagged %v, want %v", got, want)
			}
		})
	}
}

func TestLoopParallelWorkerReadsCommitInCallOrder(t *testing.T) {
	// Reverse completion order exercises timestamp and handle ordering.
	sleeps := map[string]time.Duration{"a.go": 30 * time.Millisecond, "b.go": 15 * time.Millisecond, "c.go": 0}
	reg := tools.NewStubRegistry()
	_ = reg.Register("read", func(_ context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
		path, _ := args["path"].(string)
		time.Sleep(sleeps[path])
		return "contents of " + path, nil
	})

	client := llm.NewMockProvider(&llm.MockConfig{Responses: mockToolThenDone("survey", []llm.MockToolCall{
		{ID: "tc1", Name: "read", Args: map[string]any{"path": "a.go"}},
		{ID: "tc2", Name: "read", Args: map[string]any{"path": "b.go"}},
		{ID: "tc3", Name: "read", Args: map[string]any{"path": "c.go"}},
	})})
	store := store.NewMemory()
	deps := promptloop.StoreDeps(store)
	deps.LoadedTools = workersLoaded
	deps.Limits = loopTestLimits(2)
	deps.LLM = client
	deps.Tools = reg
	deps.Policy = &recordingToolPolicy{}
	deps.CommitEvidenceToolResult = commitEvidenceFromStore(store)
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	sess.ParentSessionID = "coordinator-parent" // mark as a worker child so handles are tagged
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("survey"),
		ProfileID: "explore_readonly",
	})
	testutil.FailErr(t, "loop.Run", err)

	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "get messages", err)
	type toolRow struct {
		ts     time.Time
		handle string
		path   string
	}
	var rows []toolRow
	for _, m := range msgs {
		if m.Role != api.MessageRoleTool {
			continue
		}
		nl := strings.IndexByte(m.Content, '\n')
		if nl <= 0 || !strings.HasPrefix(m.Content, "[read#") {
			continue
		}
		rows = append(rows, toolRow{ts: m.CreatedAt, handle: m.Content[:nl], path: strings.TrimPrefix(m.Content[nl+1:], "contents of ")})
	}
	// Simulate the production store's ORDER BY ts retrieval.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ts.Before(rows[j].ts) })
	wantPaths := []string{"a.go", "b.go", "c.go"}
	wantHandles := []string{"[read#1]", "[read#2]", "[read#3]"}
	if len(rows) != 3 {
		t.Fatalf("tagged read rows = %d want 3 (%v)", len(rows), rows)
	}
	for i, r := range rows {
		if r.path != wantPaths[i] || r.handle != wantHandles[i] {
			t.Fatalf("ts-ordered read %d = (%s, %s) want (%s, %s) — batch scrambled by completion-order ts",
				i, r.handle, r.path, wantHandles[i], wantPaths[i])
		}
	}
}

func TestLoopMixedBatchSerializesWriteAfterParallelReads(t *testing.T) {
	var concurrent int32
	var peak int32
	var order []string
	var orderMu sync.Mutex
	reg := tools.NewStubRegistry()
	_ = reg.Register("read", func(ctx context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
		cur := atomic.AddInt32(&concurrent, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if cur > old && atomic.CompareAndSwapInt32(&peak, old, cur) {
				break
			}
			if cur <= old {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt32(&concurrent, -1)
		orderMu.Lock()
		order = append(order, "read")
		orderMu.Unlock()
		path, _ := args["path"].(string)
		return "contents of " + path, nil
	})
	_ = reg.Register("write", func(_ context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
		orderMu.Lock()
		order = append(order, "write")
		orderMu.Unlock()
		path, _ := args["path"].(string)
		return "wrote " + path, nil
	})

	client := llm.NewMockProvider(&llm.MockConfig{Responses: mockToolThenDone("edit", []llm.MockToolCall{
		{ID: "tc1", Name: "read", Args: map[string]any{"path": "a.go"}},
		{ID: "tc2", Name: "read", Args: map[string]any{"path": "b.go"}},
		{ID: "tc3", Name: "write", Args: map[string]any{"path": "a.go", "content": "x"}},
	})})
	store := store.NewMemory()
	deps := promptloop.StoreDeps(store)
	deps.LoadedTools = workersLoaded
	deps.Limits = loopTestLimits(2)
	deps.LLM = client
	deps.Tools = reg
	deps.Policy = &recordingToolPolicy{}
	deps.CommitEvidenceToolResult = commitEvidenceFromStore(store)
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("edit"),
		ProfileID: "explore_readonly",
	})
	testutil.FailErr(t, "loop.Run", err)
	if atomic.LoadInt32(&peak) < 2 {
		t.Fatalf("peak concurrent reads = %d want >= 2", peak)
	}
	orderMu.Lock()
	got := append([]string(nil), order...)
	orderMu.Unlock()
	if len(got) != 3 || got[0] != "read" || got[1] != "read" || got[2] != "write" {
		t.Fatalf("tool order = %v want [read read write]", got)
	}
}

func TestLoopDropsEmptyArgPhantomTaskBeforeExecution(t *testing.T) {
	var taskInvocations int32
	reg := tools.NewStubRegistry()
	_ = reg.Register("task", func(_ context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
		atomic.AddInt32(&taskInvocations, 1)
		if args["agent_type"] != "implementer" {
			t.Fatalf("agent_type = %v", args["agent_type"])
		}
		return `{"job_id":"job-1","status":"enqueued"}`, nil
	})
	client := llm.NewMockProvider(&llm.MockConfig{Responses: mockToolThenDone("spawn worker", []llm.MockToolCall{
		{ID: "tc_valid", Name: "task", Args: taskCallArgs("implementer", "work")},
		{ID: "tc_phantom", Name: "task", Args: nil},
	})})
	store := store.NewMemory()
	deps := promptloop.StoreDeps(store)
	deps.LoadedTools = workersLoaded
	deps.Limits = loopTestLimits(2)
	deps.LLM = client
	deps.Tools = reg
	deps.Policy = &recordingToolPolicy{}
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("spawn worker"),
		ProfileID: "coordinator",
	})
	testutil.FailErr(t, "loop.Run", err)
	if got := atomic.LoadInt32(&taskInvocations); got != 1 {
		t.Fatalf("task invocations = %d want 1 (phantom empty-arg call dropped)", got)
	}
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "get messages", err)
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleTool {
			continue
		}
		if strings.Contains(msg.Content, "TOOL_ARGS_INVALID") {
			t.Fatalf("unexpected reject in tool history: %s", msg.Content)
		}
	}
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleAssistant || len(msg.ToolCalls) == 0 {
			continue
		}
		if len(msg.ToolCalls) != 1 {
			t.Fatalf("assistant tool_calls = %d want 1 sanitized call", len(msg.ToolCalls))
		}
	}
}

func TestLoopPublishesClassifiedResultBeforeEvidence(t *testing.T) {
	var order []string
	var orderMu sync.Mutex
	note := func(step string) {
		orderMu.Lock()
		order = append(order, step)
		orderMu.Unlock()
	}
	reg := tools.NewStubRegistry()
	_ = reg.Register("read", func(_ context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
		path, _ := args["path"].(string)
		return "contents of " + path, nil
	})
	client := llm.NewMockProvider(&llm.MockConfig{Responses: mockToolThenDone("review", []llm.MockToolCall{
		{ID: "tc1", Name: "read", Args: map[string]any{"path": "a.go"}},
	})})
	mem := store.NewMemory()
	deps := promptloop.StoreDeps(mem)
	deps.LoadedTools = workersLoaded
	deps.Limits = loopTestLimits(2)
	deps.LLM = client
	deps.Tools = reg
	deps.Policy = &recordingToolPolicy{}
	deps.CoordinatorFrame = investigateCoordinatorContext()
	deps.AppendMessages = func(ctx context.Context, sessionID string, msgs ...api.Message) error {
		for _, msg := range msgs {
			if msg.Role == api.MessageRoleTool {
				note("append")
			}
		}
		return mem.AppendMessages(ctx, sessionID, msgs...)
	}
	deps.CommitEvidenceToolResult = func(ctx context.Context, sessionID string, sess *api.Session, toolName string, args map[string]any, content, artifactID string) (string, string, error) {
		note("evidence")
		return mem.CommitVisualEvidenceToolResult(ctx, sessionID, sess.WorkspacePath, toolName, args, content, artifactID)
	}
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID:  sess.ID,
		Session:    sess,
		History:    userHistory("review"),
		UserPrompt: "review",
		ProfileID:  "coordinator",
		ToolCtx:    tools.ToolContext{TurnSurfaceID: toolcontract.SurfaceImplementInvestigate},
	})
	testutil.FailErr(t, "loop.Run", err)
	if len(order) < 2 || order[0] != "append" || order[1] != "evidence" {
		t.Fatalf("commit order = %v want append before evidence", order)
	}
}

func TestLoopPublishesParallelResultAsEachFinishes(t *testing.T) {
	fastAppended := make(chan struct{})
	var once sync.Once
	slowStarted := make(chan struct{})
	releaseSlow := make(chan struct{})
	reg := tools.NewStubRegistry()
	_ = reg.Register("read", func(_ context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
		path, _ := args["path"].(string)
		if path == "slow.go" {
			close(slowStarted)
			<-releaseSlow
			return "contents of slow.go", nil
		}
		return "contents of " + path, nil
	})
	client := llm.NewMockProvider(&llm.MockConfig{Responses: mockToolThenDone("survey", []llm.MockToolCall{
		{ID: "tc-slow", Name: "read", Args: map[string]any{"path": "slow.go"}},
		{ID: "tc-fast", Name: "read", Args: map[string]any{"path": "fast.go"}},
	})})
	mem := store.NewMemory()
	deps := promptloop.StoreDeps(mem)
	deps.LoadedTools = workersLoaded
	deps.Limits = loopTestLimits(2)
	deps.LLM = client
	deps.Tools = reg
	deps.Policy = &recordingToolPolicy{}
	deps.CommitEvidenceToolResult = commitEvidenceFromStore(mem)
	deps.AppendMessages = func(ctx context.Context, sessionID string, msgs ...api.Message) error {
		err := mem.AppendMessages(ctx, sessionID, msgs...)
		for _, msg := range msgs {
			if msg.Role == api.MessageRoleTool && msg.ToolResult != nil &&
				msg.ToolResult.ToolCallID == "tc-fast" {
				once.Do(func() { close(fastAppended) })
			}
		}
		return err
	}
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	sess.ParentSessionID = "coordinator-parent"
	done := make(chan error, 1)
	go func() {
		_, runErr := loop.Run(ctx, promptloop.PromptRunInput{
			SessionID: sess.ID,
			Session:   sess,
			History:   userHistory("survey"),
			ProfileID: "explore_readonly",
		})
		done <- runErr
	}()
	select {
	case <-slowStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("slow sibling never started")
	}
	select {
	case <-fastAppended:
	case <-time.After(2 * time.Second):
		t.Fatal("fast sibling was not appended before the slow sibling finished")
	}
	close(releaseSlow)
	testutil.FailErr(t, "loop.Run", <-done)
}
