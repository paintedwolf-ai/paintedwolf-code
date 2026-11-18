package promptloop_test

import (
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/toolstest"

	"context"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
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
	synctest.Test(t, func(t *testing.T) {
		var concurrent int32
		var peak int32
		reg := toolstest.NewRegistry()
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
		deps.Context.LoadedTools = workersLoaded
		deps.Context.Limits = loopTestLimits(2)
		deps.Model.LLM = client
		deps.Context.Tools = reg
		deps.Context.Policy = &recordingToolPolicy{}
		deps.Context.CoordinatorFrame = investigateCoordinatorContext()
		deps.Tools.CommitEvidenceToolResult = commitEvidenceFromStore(store)
		loop := promptloop.NewPromptLoopForTest(deps)
		ctx := context.Background()
		sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
		testutil.FailErr(t, "create session", err)
		_, err = loop.Run(ctx, promptloop.PromptRunInput{
			SessionID: sess.ID,
			Session:   sess,
			History:   userHistory("survey"),
			ProfileID: "explore_readonly",
		})
		testutil.FailErr(t, "loop.Run", err)
		if atomic.LoadInt32(&peak) < 2 {
			t.Fatalf("peak concurrent reads = %d want >= 2", peak)
		}
	})
}

func TestLoopCoordinatorReadsGetHandlesAcrossSurfaces(t *testing.T) {
	for _, surface := range []string{toolcontract.SurfaceImplementInvestigate, "orchestrate_plan", "implement_overlay_promote"} {
		t.Run(surface, func(t *testing.T) {
			reg := toolstest.NewRegistry()
			_ = reg.Register("command", func(_ context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
				path, _ := args["path"].(string)
				return "contents of " + path, nil
			})
			client := llm.NewMockProvider(&llm.MockConfig{Responses: mockToolThenDone("review the code", []llm.MockToolCall{
				{ID: "tc1", Name: "command", Args: map[string]any{"path": "a.go", "command": "cat a.go"}},
				{ID: "tc2", Name: "command", Args: map[string]any{"path": "b.go", "command": "cat b.go"}},
			})})
			store := store.NewMemory()
			deps := promptloop.StoreDeps(store)
			deps.Context.LoadedTools = workersLoaded
			// The final iteration is prose-only.
			deps.Context.Limits = loopTestLimits(2)
			deps.Model.LLM = client
			deps.Context.Tools = reg
			deps.Context.Policy = &recordingToolPolicy{}
			deps.Context.CoordinatorFrame = staticCoordinatorContext{run: api.CoordinatorRunContext{WorkflowID: "release", CurrentPhase: "record", PhaseCoordinatorSurface: surface}}
			deps.Tools.CommitEvidenceToolResult = commitEvidenceFromStore(store)
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
				ToolCtx: tools.ToolContext{
					Turn: tools.InvocationTurn{TurnSurfaceID: surface},
				},
			})
			testutil.FailErr(t, "loop.Run", err)

			msgs, err := store.GetMessages(ctx, sess.ID)
			testutil.FailErr(t, "get messages", err)
			got := map[string]string{}
			for _, m := range msgs {
				if m.Role != api.MessageRoleTool || m.ToolResult == nil {
					continue
				}
				if nl := strings.IndexByte(m.Content, '\n'); nl > 0 && strings.HasPrefix(m.Content, "[command#") {
					path, _ := m.ToolResult.ToolArgs["path"].(string)
					got[path] = m.Content[:nl]
				}
			}
			// Parallel calls take handles as they finish.
			if handles := []string{got["a.go"], got["b.go"]}; !slices.Contains(handles, "[command#1]") || !slices.Contains(handles, "[command#2]") {
				t.Fatalf("coordinator reads tagged %v, want command#1 and command#2", got)
			}
		})
	}
}

func TestLoopParallelWorkerReadsCommitInCallOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Reverse completion order exercises timestamp and handle ordering.
		sleeps := map[string]time.Duration{"a.go": 30 * time.Millisecond, "b.go": 15 * time.Millisecond, "c.go": 0}
		reg := toolstest.NewRegistry()
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
		deps.Context.LoadedTools = workersLoaded
		deps.Context.Limits = loopTestLimits(2)
		deps.Model.LLM = client
		deps.Context.Tools = reg
		deps.Context.Policy = &recordingToolPolicy{}
		deps.Tools.CommitEvidenceToolResult = commitEvidenceFromStore(store)
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
			ord    int64
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
			rows = append(rows, toolRow{ts: m.CreatedAt, ord: m.Ord, handle: m.Content[:nl], path: strings.TrimPrefix(m.Content[nl+1:], "contents of ")})
		}
		if len(rows) != 3 {
			t.Fatalf("tagged read rows = %d want 3 (%v)", len(rows), rows)
		}
		// Timestamps keep call order.
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].ts.Before(rows[j].ts) })
		for i, want := range []string{"a.go", "b.go", "c.go"} {
			if rows[i].path != want {
				t.Fatalf("ts-ordered read %d = %s want %s — batch scrambled by completion-order ts", i, rows[i].path, want)
			}
		}
		// Handles are minted as each result is stored, so they follow the stored order.
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].ord < rows[j].ord })
		for i, want := range []string{"[read#1]", "[read#2]", "[read#3]"} {
			if rows[i].handle != want {
				t.Fatalf("read stored %d carries %s want %s", i, rows[i].handle, want)
			}
		}
	})
}

func TestLoopMixedBatchSerializesWriteAfterParallelReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var concurrent int32
		var peak int32
		var order []string
		var orderMu sync.Mutex
		reg := toolstest.NewRegistry()
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
		deps.Context.LoadedTools = workersLoaded
		deps.Context.Limits = loopTestLimits(2)
		deps.Model.LLM = client
		deps.Context.Tools = reg
		deps.Context.Policy = &recordingToolPolicy{}
		deps.Tools.CommitEvidenceToolResult = commitEvidenceFromStore(store)
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
	})
}

func TestLoopDropsEmptyArgPhantomTaskBeforeExecution(t *testing.T) {
	var taskInvocations int32
	reg := toolstest.NewRegistry()
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
	deps.Context.LoadedTools = workersLoaded
	deps.Context.Limits = loopTestLimits(2)
	deps.Model.LLM = client
	deps.Context.Tools = reg
	deps.Context.Policy = &recordingToolPolicy{}
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

// A result is stored once, already carrying its evidence handle, so no crash
// can leave it stored without one.
func TestLoopStoresClassifiedResultWithItsEvidence(t *testing.T) {
	var order []string
	var orderMu sync.Mutex
	note := func(step string) {
		orderMu.Lock()
		order = append(order, step)
		orderMu.Unlock()
	}
	reg := toolstest.NewRegistry()
	_ = reg.Register("command", func(_ context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
		path, _ := args["path"].(string)
		return "contents of " + path, nil
	})
	client := llm.NewMockProvider(&llm.MockConfig{Responses: mockToolThenDone("review", []llm.MockToolCall{
		{ID: "tc1", Name: "command", Args: map[string]any{"path": "a.go", "command": "cat a.go"}},
	})})
	mem := store.NewMemory()
	deps := promptloop.StoreDeps(mem)
	deps.Context.LoadedTools = workersLoaded
	deps.Context.Limits = loopTestLimits(2)
	deps.Model.LLM = client
	deps.Context.Tools = reg
	deps.Context.Policy = &recordingToolPolicy{}
	deps.Context.CoordinatorFrame = investigateCoordinatorContext()
	deps.Projection.AppendMessages = func(ctx context.Context, sessionID string, msgs ...api.Message) error {
		for _, msg := range msgs {
			if msg.Role == api.MessageRoleTool {
				note("append " + strings.Join(msg.EvidenceHandles, ","))
			}
		}
		return mem.AppendMessages(ctx, sessionID, msgs...)
	}
	deps.Tools.CommitEvidenceToolResult = func(ctx context.Context, sessionID string, sess *api.Session, toolName string, args map[string]any, content, artifactID string) (string, string, error) {
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
		ToolCtx: tools.ToolContext{
			Turn: tools.InvocationTurn{TurnSurfaceID: toolcontract.SurfaceImplementInvestigate},
		},
	})
	testutil.FailErr(t, "loop.Run", err)
	if !slices.Equal(order, []string{"evidence", "append command#1"}) {
		t.Fatalf("commit order = %v want the evidence, then the result appended with its handle", order)
	}
}

func TestLoopPublishesParallelResultAsEachFinishes(t *testing.T) {
	fastAppended := make(chan struct{})
	var once sync.Once
	slowStarted := make(chan struct{})
	releaseSlow := make(chan struct{})
	reg := toolstest.NewRegistry()
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
	deps.Context.LoadedTools = workersLoaded
	deps.Context.Limits = loopTestLimits(2)
	deps.Model.LLM = client
	deps.Context.Tools = reg
	deps.Context.Policy = &recordingToolPolicy{}
	deps.Tools.CommitEvidenceToolResult = commitEvidenceFromStore(mem)
	deps.Projection.AppendMessages = func(ctx context.Context, sessionID string, msgs ...api.Message) error {
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
	case <-t.Context().Done():
		t.Fatal("slow sibling never started")
	}
	select {
	case <-fastAppended:
	case <-t.Context().Done():
		t.Fatal("fast sibling was not appended before the slow sibling finished")
	}
	close(releaseSlow)
	testutil.FailErr(t, "loop.Run", <-done)
}
