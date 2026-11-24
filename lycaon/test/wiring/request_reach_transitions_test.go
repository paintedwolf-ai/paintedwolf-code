package wiring

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/llmtest"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAskAndUpgradeTransitionsMaintainRequestTruth(t *testing.T) {
	oracle := newReachOracle(t)
	t.Run("ask_first_resumed_and_refusals", func(t *testing.T) {
		runAskFirstAndResumedScenario(t, oracle)
	})
	t.Run("mid_turn_ask_to_agent_with_readonly_child_and_bg_job", func(t *testing.T) {
		runMidTurnAskUpgradeScenario(t, oracle)
	})
	t.Run("between_turn_ask_to_agent_upgrade", func(t *testing.T) {
		runBetweenTurnAskUpgradeScenario(t, oracle)
	})
}

func runAskFirstAndResumedScenario(t *testing.T, oracle *reachOracle) {
	t.Helper()
	var round atomic.Int32
	rec := llmtest.NewRecordingClient(llmtest.NewKickDrivenMock(llmtest.KickDrivenConfig{
		Fallback: func(_ context.Context, _ llmtest.Snapshot) modelcall.Completion {
			switch round.Add(1) {
			case 1:
				return askRoundToolCall("r1-read", "inspect_source", map[string]any{"operation": "read", "path": "note.txt"})
			case 2:
				return askRoundToolCall("r2-write", "write", map[string]any{"path": "bad.txt", "content": "denied"})
			case 3:
				return askRoundToolCall("r3-task", "task", TaskToolArgs(orchestration.ProfileImplementer, "Edit note.txt", "note.txt"))
			case 4:
				return modelcall.Completion{Content: "First Ask turn settled without mutations."}
			case 5:
				return askRoundToolCall("r5-read", "inspect_source", map[string]any{"operation": "read", "path": "note.txt"})
			default:
				return modelcall.Completion{Content: "Resumed Ask turn settled cleanly."}
			}
		},
	}))
	h := BuildForTest(t, WithLLMClient(rec), WithDecider(decidetest.LoadAll{}))
	ctx := h.OwnerCtx(t, t.Context())
	dir := h.ProjectDir(t, "ask-first-resumed")
	testutil.FailErr(t, "seed note", os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello ask\n"), 0o600))
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Mode: new(api.SessionModeAsk)}, dir)
	testutil.FailErr(t, "create Ask session", err)
	h.Start(t)

	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Inspect note.txt and test Ask refusals.")
	testutil.FailErr(t, "first Ask prompt", err)
	WaitForSessionIdleGate(t, h, ctx, sess.ID)

	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Resume Ask inspection.")
	testutil.FailErr(t, "resumed Ask prompt", err)
	WaitForSessionIdleGate(t, h, ctx, sess.ID)

	msgs, err := h.Store.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "load transcript", err)
	if got := countAgentEnabledSignals(msgs); got != 0 {
		t.Fatalf("unupgraded Ask session emitted %d session.mode.agent_enabled notices, want 0", got)
	}

	stored, err := h.Store.Sessions.Get(ctx, sess.ID)
	testutil.FailErr(t, "reload Ask session", err)
	profiles := h.Sessions.Manager.Profiles.ForSession(ctx, stored)
	var codes []string
	for i, req := range conversationRequests(rec) {
		for _, m := range req.Messages {
			if m.ToolResult != nil && m.ToolResult.Outcome == api.ToolResultOutcomeRejected {
				codes = append(codes, m.ToolResult.Codes...)
			}
		}
		if viols := oracle.check(t, req, profiles); len(viols) > 0 {
			t.Fatalf("Ask request %d failed reach check:\n%s", i, strings.Join(viols, "\n"))
		}
	}
	if !slices.Contains(codes, "DISALLOWED_AGENT") && !slices.Contains(codes, tools.AskModeActiveCode) {
		t.Fatalf("expected DISALLOWED_AGENT or %s among Ask refusal codes, got %v", tools.AskModeActiveCode, codes)
	}
}

func runMidTurnAskUpgradeScenario(t *testing.T, oracle *reachOracle) {
	t.Helper()
	var h *Harness
	var sessID string
	var round atomic.Int32
	rec := llmtest.NewRecordingClient(llmtest.NewKickDrivenMock(llmtest.KickDrivenConfig{
		Fallback: func(ctx context.Context, snap llmtest.Snapshot) modelcall.Completion {
			if strings.Contains(snap.LastUserMessage, "Read note.txt in Ask mode.") {
				return modelcall.Completion{Content: MockWorkerCompletionJSON("complete", "Read note.txt cleanly.", []string{"Inspected repository in read scope."})}
			}
			switch round.Add(1) {
			case 1:
				return askRoundToolCall("ask-read", "inspect_source", map[string]any{"operation": "read", "path": "note.txt"})
			case 2:
				if _, err := h.Sessions.Manager.Chats.SetMode(ctx, sessID, api.SessionModeAgent); err != nil {
					t.Errorf("mid-turn SetMode(Agent): %v", err)
				}
				return askRoundToolCall("read-before-refresh", "inspect_source", map[string]any{"operation": "read", "path": "note.txt"})
			case 3:
				return askRoundToolCall("plan", "update_progress", h.ProgressUpdateArgs(t, ctx, sessID, "## Progress\n- [ ] write upgraded.txt\n"))
			case 4:
				return askRoundToolCall("cmd-write", "command", map[string]any{"command": "printf 'upgraded\\n' > upgraded.txt"})
			case 5:
				return askRoundToolCall("plan-done", "update_progress", h.ProgressUpdateArgs(t, ctx, sessID, "## Progress\n- [x] write upgraded.txt\n"))
			default:
				return modelcall.Completion{Content: MockCoordinatorCloseoutMarkdown("Upgraded mid-turn and wrote upgraded.txt.", "")}
			}
		},
	}))
	h = BuildForTest(t, WithLLMClient(rec), WithDecider(decidetest.LoadAll{}))
	ctx := h.OwnerCtx(t, t.Context())
	dir := h.ProjectDir(t, "ask-midturn-upgrade")
	WriteTestProjectApprovalsAllowWrite(t, dir)
	testutil.FailErr(t, "seed note", os.WriteFile(filepath.Join(dir, "note.txt"), []byte("ask seed\n"), 0o600))
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Mode: new(api.SessionModeAsk)}, dir)
	testutil.FailErr(t, "create Ask session", err)
	sessID = sess.ID
	h.Start(t)

	// Start a read-only background Ask ToolContext and an Ask-spawned read worker before the root session upgrades.
	bgTC := WiringToolContext(sess.ID, dir)
	bgTC.Identity.ProjectID, bgTC.Identity.RootSessionID, bgTC.Identity.Agent = sess.ProjectID, sess.ID, "coordinator"
	testutil.FailErr(t, "resolve primary root for bgTC", h.DB.QueryRowContext(ctx, "SELECT id FROM project_roots WHERE project_id = ? AND is_primary = 1", sess.ProjectID).Scan(&bgTC.Source.ActiveRootID))
	bgTC.Source.Roots[0].ID = bgTC.Source.ActiveRootID
	bgTC.Source.AskMode, bgTC.Source.ReadOnly = true, true
	bgTC.Identity.ToolCallID = "ask-bg-job"
	bgOut, bgErr := h.ToolRegistry.Run(ctx, "pack_board", map[string]any{"detail_level": "compact"}, bgTC)
	testutil.FailErr(t, "run read-only Ask background context before upgrade", bgErr)
	if strings.TrimSpace(bgOut) == "" {
		t.Fatal("expected non-empty pack_board output from pre-upgrade read-only Ask context")
	}

	bgTC.Identity.ToolCallID = "spawn-ask-scout"
	scoutArgs := TaskToolArgs(orchestration.ProfileRepoResearcher, "Read note.txt in Ask mode.")
	scoutArgs["name"] = "ask-scout"
	_, spawnErr := h.ToolRegistry.Run(ctx, "task", scoutArgs, bgTC)
	testutil.FailErr(t, "spawn ask-scout in Ask mode", spawnErr)
	testutil.WaitFor(t, "ask-scout round 1 quiescent", func() bool {
		return workerCycleQuiescent(ctx, h, sess.ProjectID, sess.ID)
	})
	FailOnEndedWorker(t, h, ctx, sess.ProjectID)
	WaitForSessionIdleGate(t, h, ctx, sess.ID)

	bgTC.Identity.ToolCallID = "deny-escalate-in-ask"
	if _, escErr := h.ToolRegistry.Run(ctx, "instruct_worker", map[string]any{
		"worker": "ask-scout", "instructions": "Rewrite note.txt.", "agent_type": orchestration.ProfileImplementer,
	}, bgTC); escErr == nil {
		t.Fatal("expected instruct_worker escalating Ask-spawned read worker to implementer under Ask ToolContext to fail")
	}

	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Spawn ask-scout, upgrade mid-turn, and write upgraded.txt.")
	testutil.FailErr(t, "mid-turn upgrade prompt", err)
	WaitForSessionIdleGate(t, h, ctx, sess.ID)

	// Run a follow-up Agent prompt to confirm session.mode.agent_enabled is never duplicated.
	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Confirm post-upgrade follow-up turn.")
	testutil.FailErr(t, "post-upgrade follow-up prompt", err)
	WaitForSessionIdleGate(t, h, ctx, sess.ID)

	verifyPostUpgradeChildRetentionAndRequests(t, h, ctx, oracle, rec, sess, dir, bgTC)
}

func verifyPostUpgradeChildRetentionAndRequests(
	t *testing.T,
	h *Harness,
	ctx context.Context,
	oracle *reachOracle,
	rec *llmtest.RecordingClient,
	sess *api.Session,
	dir string,
	bgTC tools.ToolContext,
) {
	t.Helper()
	// Background context started in Ask retains read-only enforcement after root upgrade.
	bgTC.Identity.ToolCallID = "ask-bg-read-after-upgrade"
	if _, readErr := h.ToolRegistry.Run(ctx, "pack_board", map[string]any{"detail_level": "compact"}, bgTC); readErr != nil {
		t.Fatalf("expected pre-upgrade read-only Ask ToolContext to retain read access: %v", readErr)
	}
	bgTC.Identity.ToolCallID = "ask-bg-mutate-after-upgrade"
	if _, mutErr := h.ToolRegistry.Run(ctx, "command", map[string]any{"command": "printf 'leak\\n' > bg_leak.txt"}, bgTC); mutErr == nil {
		t.Fatal("expected pre-upgrade read-only background ToolContext to reject file mutation after root upgrade")
	}

	// Ask-spawned child worker retains scope_mode=read across root upgrade and continues in read scope.
	var scoutName, agentType, scopeMode string
	testutil.FailErr(t, "query Ask-spawned worker name", h.DB.QueryRowContext(ctx,
		"SELECT name, agent_type, scope_mode FROM workers WHERE session_id = ? LIMIT 1", sess.ID).Scan(&scoutName, &agentType, &scopeMode))
	if agentType != orchestration.ProfileRepoResearcher || scopeMode != "read" {
		t.Fatalf("ask-scout mutated by root upgrade alone: agent_type=%s scope_mode=%s", agentType, scopeMode)
	}
	agentTC := WiringToolContext(sess.ID, dir)
	agentTC.Identity.ProjectID, agentTC.Identity.RootSessionID, agentTC.Identity.Agent = sess.ProjectID, sess.ID, "coordinator"
	testutil.FailErr(t, "resolve primary root", h.DB.QueryRowContext(ctx, "SELECT id FROM project_roots WHERE project_id = ? AND is_primary = 1", sess.ProjectID).Scan(&agentTC.Source.ActiveRootID))
	agentTC.Source.Roots[0].ID = agentTC.Source.ActiveRootID

	// Continuing ask-scout as repo-researcher after root upgrade keeps it in read scope.
	agentTC.Identity.ToolCallID = "continue-ask-scout-readonly"
	_, contErr := h.ToolRegistry.Run(ctx, "instruct_worker", map[string]any{
		"worker": scoutName, "instructions": "Read note.txt in Ask mode. Follow-up check.",
	}, agentTC)
	testutil.FailErr(t, "continue ask-scout in read scope", contErr)
	testutil.WaitFor(t, "ask-scout round 2 quiescent", func() bool {
		return workerCycleQuiescent(ctx, h, sess.ProjectID, sess.ID)
	})
	FailOnEndedWorker(t, h, ctx, sess.ProjectID)
	WaitForSessionIdleGate(t, h, ctx, sess.ID)

	bgTC.Identity.ToolCallID = "deny-escalate-after-upgrade"
	if _, escErr := h.ToolRegistry.Run(ctx, "instruct_worker", map[string]any{
		"worker": scoutName, "instructions": "Rewrite note.txt.", "agent_type": orchestration.ProfileImplementer,
	}, bgTC); escErr == nil {
		t.Fatal("expected retained Ask ToolContext to reject escalating ask-scout to implementer after root upgrade")
	}

	testutil.FailErr(t, "query ask-scout record", h.DB.QueryRowContext(ctx,
		"SELECT agent_type, scope_mode FROM workers WHERE session_id = ? AND name = ?", sess.ID, scoutName).Scan(&agentType, &scopeMode))
	if agentType != orchestration.ProfileRepoResearcher || scopeMode != "read" {
		t.Fatalf("ask-scout mutated after root upgrade: agent_type=%s scope_mode=%s", agentType, scopeMode)
	}

	msgs, err := h.Store.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "load upgraded transcript", err)
	stored, err := h.Store.Sessions.Get(ctx, sess.ID)
	testutil.FailErr(t, "reload session", err)
	profiles := h.Sessions.Manager.Profiles.ForSession(ctx, stored)

	reqs := conversationRequests(rec)
	if viols := verifyAskUpgradeInvariants(msgs, reqs, orchestration.ProfileRepoResearcher); len(viols) > 0 {
		t.Fatalf("mid-turn upgrade transition invariant violations:\n%s", strings.Join(viols, "\n"))
	}
	scoutReqs := 0
	for i, req := range reqs {
		if req.Debug.AgentType == orchestration.ProfileRepoResearcher {
			scoutReqs++
		}
		if viols := oracle.check(t, req, profiles); len(viols) > 0 {
			t.Fatalf("mid-turn upgrade request %d (%s/%s) failed reach check:\n%s", i, req.Debug.Surface, req.Debug.AgentType, strings.Join(viols, "\n"))
		}
	}
	if scoutReqs < 2 {
		t.Fatalf("expected >= 2 recorded repo-researcher requests (pre- and post-upgrade), got %d", scoutReqs)
	}
}

func runBetweenTurnAskUpgradeScenario(t *testing.T, oracle *reachOracle) {
	t.Helper()
	var h *Harness
	var sessID string
	var round atomic.Int32
	rec := llmtest.NewRecordingClient(llmtest.NewKickDrivenMock(llmtest.KickDrivenConfig{
		Fallback: func(ctx context.Context, _ llmtest.Snapshot) modelcall.Completion {
			switch round.Add(1) {
			case 1:
				return askRoundToolCall("ask-mut", "task", TaskToolArgs(orchestration.ProfileImplementer, "Try mutating in Ask", "note.txt"))
			case 2:
				return modelcall.Completion{Content: "Ask turn recorded refusal."}
			case 3:
				return askRoundToolCall("agent-plan", "update_progress", h.ProgressUpdateArgs(t, ctx, sessID, "## Progress\n- [ ] write between.txt\n"))
			case 4:
				return askRoundToolCall("agent-cmd", "command", map[string]any{"command": "printf 'between\\n' > between.txt"})
			case 5:
				return askRoundToolCall("agent-done", "update_progress", h.ProgressUpdateArgs(t, ctx, sessID, "## Progress\n- [x] write between.txt\n"))
			case 6:
				return modelcall.Completion{Content: MockCoordinatorCloseoutMarkdown("Wrote between.txt after upgrade.", "")}
			default:
				return modelcall.Completion{Content: "Third turn settled."}
			}
		},
	}))
	h = BuildForTest(t, WithLLMClient(rec), WithDecider(decidetest.LoadAll{}))
	ctx := h.OwnerCtx(t, t.Context())
	dir := h.ProjectDir(t, "ask-betweenturn-upgrade")
	WriteTestProjectApprovalsAllowWrite(t, dir)
	testutil.FailErr(t, "seed note", os.WriteFile(filepath.Join(dir, "note.txt"), []byte("between\n"), 0o600))
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Mode: new(api.SessionModeAsk)}, dir)
	testutil.FailErr(t, "create Ask session", err)
	sessID = sess.ID
	h.Start(t)

	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Attempt mutating task in Ask.")
	testutil.FailErr(t, "Ask turn", err)
	WaitForSessionIdleGate(t, h, ctx, sess.ID)

	_, err = h.Sessions.Manager.Chats.SetMode(ctx, sess.ID, api.SessionModeAgent)
	testutil.FailErr(t, "between-turn SetMode(Agent)", err)

	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Now write between.txt in Agent mode.")
	testutil.FailErr(t, "first Agent turn after between-turn upgrade", err)
	WaitForSessionIdleGate(t, h, ctx, sess.ID)

	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Confirm no duplicate transition notice.")
	testutil.FailErr(t, "second Agent turn", err)
	WaitForSessionIdleGate(t, h, ctx, sess.ID)

	msgs, err := h.Store.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "load transcript", err)
	if got := countAgentEnabledSignals(msgs); got != 1 {
		t.Fatalf("between-turn upgrade session.mode.agent_enabled count = %d, want 1", got)
	}
	stored, err := h.Store.Sessions.Get(ctx, sess.ID)
	testutil.FailErr(t, "reload session", err)
	profiles := h.Sessions.Manager.Profiles.ForSession(ctx, stored)
	for i, req := range conversationRequests(rec) {
		if viols := oracle.check(t, req, profiles); len(viols) > 0 {
			t.Fatalf("between-turn upgrade request %d failed reach check:\n%s", i, strings.Join(viols, "\n"))
		}
	}
}

func verifyAskUpgradeInvariants(
	transcript []api.Message,
	requests []modelcall.CompletionRequest,
	readOnlyChildAgent string,
) []string {
	var out []string
	signals := countAgentEnabledSignals(transcript)
	if signals != 1 {
		out = append(out, fmt.Sprintf(
			"session.mode.agent_enabled [self_now] (upgraded_ask_to_agent): expected 1 one-shot transition notice, got %d",
			signals,
		))
	}
	for i, req := range requests {
		if req.Debug.AgentType != readOnlyChildAgent {
			continue
		}
		for _, meta := range req.Tools {
			if toolcontract.MutatesContent(meta.Name) {
				out = append(out, fmt.Sprintf(
					"worker/%s [self_now] (scope_mode=read, request=%d): Ask-spawned child offered mutating tool `%s`",
					readOnlyChildAgent, i, meta.Name,
				))
			}
			if meta.Name == "command" && argFields(meta.ArgsSchema)["verification"] {
				out = append(out, fmt.Sprintf(
					"worker/%s [self_now] (scope_mode=read, request=%d): Ask-spawned child offered unpruned `command.verification`",
					readOnlyChildAgent, i,
				))
			}
		}
	}
	return out
}

// Ask requests withhold command, offer task only for read-only agents, and
// project ask_user without secret responses.
func TestAskModeProjectedRequests(t *testing.T) {
	oracle := newReachOracle(t)
	var round atomic.Int32
	rec := llmtest.NewRecordingClient(llmtest.NewKickDrivenMock(llmtest.KickDrivenConfig{
		Fallback: func(_ context.Context, _ llmtest.Snapshot) modelcall.Completion {
			if round.Add(1) == 1 {
				return askRoundToolCall("load-task", "request_tools", map[string]any{"need": "task"})
			}
			return modelcall.Completion{Content: "Inspected the Ask surface."}
		},
	}))
	h := BuildForTest(t, WithLLMClient(rec), WithDecider(&decidetest.Fake{}))
	ctx := h.OwnerCtx(t, t.Context())
	dir := h.ProjectDir(t, "ask-projected")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Mode: new(api.SessionModeAsk)}, dir)
	testutil.FailErr(t, "create Ask session", err)
	h.Start(t)

	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Load task in Ask.")
	testutil.FailErr(t, "prompt Ask", err)
	WaitForSessionIdleGate(t, h, ctx, sess.ID)

	reqs := conversationRequests(rec)
	if len(reqs) < 2 {
		t.Fatalf("expected >= 2 requests in the Ask turn, got %d", len(reqs))
	}
	postLoadReq := reqs[len(reqs)-1]
	if _, hasCmd := toolMetaSchemaInRequest(postLoadReq, "command"); hasCmd {
		t.Fatal("Ask request offered command")
	}
	taskSchema, hasTask := toolMetaSchemaInRequest(postLoadReq, "task")
	if !hasTask {
		t.Fatalf("Ask post-load request missing task tool: %v", postLoadReq.Tools)
	}
	for p, vals := range toolvocabtest.SchemaEnums(taskSchema) {
		if strings.HasSuffix(p, "agent_type") {
			for _, agentID := range vals {
				if mut, ok := prompts.AgentMutationCapable(agentID); ok && mut {
					t.Fatalf("Ask task schema offered mutating agent %q", agentID)
				}
			}
		}
	}

	// A read-only workflow surfaces ask_user in Ask mode.
	_, err = h.Workflows.Manager.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "recon-pack", WorkflowVersion: activeWorkflowVersion("recon-pack"),
	})
	testutil.FailErr(t, "start recon-pack in Ask", err)
	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Inspect Ask workflow ask_user projection.")
	testutil.FailErr(t, "prompt Ask workflow", err)
	WaitForSessionIdleGate(t, h, ctx, sess.ID)

	reqs = conversationRequests(rec)
	wfReq := reqs[len(reqs)-1]
	askSchema, hasAsk := toolMetaSchemaInRequest(wfReq, "ask_user")
	if !hasAsk {
		t.Fatalf("Ask workflow request missing ask_user schema: %v", wfReq.Tools)
	}
	for p, vals := range toolvocabtest.SchemaEnums(askSchema) {
		if strings.HasSuffix(p, "response_type") && slices.Contains(vals, "secret") {
			t.Fatalf("Ask ask_user schema retained secret response_type: %v", vals)
		}
	}
	stored, err := h.Store.Sessions.Get(ctx, sess.ID)
	testutil.FailErr(t, "reload Ask session", err)
	profiles := h.Sessions.Manager.Profiles.ForSession(ctx, stored)
	for i, req := range reqs {
		if viols := oracle.check(t, req, profiles); len(viols) > 0 {
			t.Fatalf("Ask request %d failed reach check:\n%s", i, strings.Join(viols, "\n"))
		}
	}
}

type fixedWindowBudget struct {
	window int
}

func (b fixedWindowBudget) RequestBudget(_ context.Context, _ string, req modelcall.CompletionRequest) modelcall.RequestBudget {
	return modelcall.RequestBudget{
		Window:   b.window,
		Input:    modelcall.EstimateRequestInput(req),
		Output:   256,
		Headroom: 128,
		Exact:    true,
	}
}

// FitRequest's compaction names recall only as the request offers it.
func TestCompactionFitRequestNamesOnlyOfferedRecall(t *testing.T) {
	oracle := newReachOracle(t)
	rec := llmtest.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{
		Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "Compacted summary ready."}},
	}))
	h := BuildForTest(t, WithLLMClient(rec), WithDecider(decide.Absent{}))
	ctx := h.OwnerCtx(t, t.Context())
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{ProviderID: "mock", Model: "mock"}, t.TempDir())
	testutil.FailErr(t, "create compaction session", err)

	var rows []api.Message
	for i := 0; i < 6; i++ {
		callID := fmt.Sprintf("cmd-%d", i)
		rows = append(rows,
			api.Message{ID: fmt.Sprintf("u-%d", i), Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser, Content: fmt.Sprintf("Step %d request", i)},
			api.Message{ID: fmt.Sprintf("a-%d", i), Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: callID, Name: "command", Args: map[string]any{"command": "ls"}}}},
			api.Message{ID: fmt.Sprintf("t-%d", i), Role: api.MessageRoleTool, Origin: api.MessageOriginTool, Content: strings.Repeat(fmt.Sprintf("output line %d\n", i), 240), ToolResult: &api.ToolResult{ToolCallID: callID, Tool: "command", Outcome: api.ToolResultOutcomeCompleted}},
		)
	}
	rows = append(rows, api.Message{
		ID: "u-final", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser,
		Content:      "Summarize findings from the earlier command outputs.",
		ContentParts: []api.MessageContentPart{{Content: "Summarize findings from the earlier command outputs.", Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser}},
	})
	testutil.FailErr(t, "append transcript rows", h.Store.Transcript.AppendMessages(ctx, sess.ID, rows...))
	canonical, err := h.Store.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read canonical rows", err)

	standing := api.Message{ID: "standing", Role: api.MessageRoleSystem, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, Content: "Use only offered tools.", ContextPinned: true, PromptCacheBreakpoint: api.PromptCacheTierStanding}
	baseMessages := append([]api.Message{standing}, canonical...)
	profiles := h.Sessions.Manager.Profiles.ForSession(ctx, sess)

	// recall withheld from the request though the session wires it.
	withheldReq := modelcall.CompletionRequest{
		Model: sess.Model, Messages: slices.Clone(baseMessages),
		Tools: []tools.ToolMeta{{Name: "command", ArgsSchema: map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}}}},
		Debug: modelcall.RequestDebug{ProfileID: prompts.CoordinatorProfileID, Surface: "implement_investigate"},
	}
	fittedWithheld, err := h.Sessions.Manager.Runner.History.FitRequest(ctx, sess, canonical, withheldReq, fixedWindowBudget{window: 2400}, nil)
	testutil.FailErr(t, "FitRequest without recall", err)
	if viols := oracle.check(t, fittedWithheld, profiles); len(viols) > 0 {
		t.Fatalf("FitRequest without recall leaked unreachable tool claims:\n%s", strings.Join(viols, "\n"))
	}

	// recall offered with a query-only schema.
	queryOnlyRecall := tools.ToolMeta{
		Name: "recall",
		ArgsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
			},
		},
	}
	offeredReq := withheldReq
	offeredReq.Messages = slices.Clone(baseMessages)
	offeredReq.Tools = append(slices.Clone(withheldReq.Tools), queryOnlyRecall)
	fittedOffered, err := h.Sessions.Manager.Runner.History.FitRequest(ctx, sess, canonical, offeredReq, fixedWindowBudget{window: 2400}, nil)
	testutil.FailErr(t, "FitRequest with query-only recall", err)
	if viols := oracle.check(t, fittedOffered, profiles); len(viols) > 0 {
		t.Fatalf("FitRequest with query-only recall failed reach/schema check:\n%s", strings.Join(viols, "\n"))
	}
}

func conversationRequests(rec *llmtest.RecordingClient) []modelcall.CompletionRequest {
	var out []modelcall.CompletionRequest
	for _, req := range rec.AllRequests() {
		if req.Composition == modelcall.CompositionConversation {
			out = append(out, req)
		}
	}
	return out
}

func countAgentEnabledSignals(messages []api.Message) int {
	n := 0
	for _, m := range messages {
		if m.HostSignalID == "session.mode.agent_enabled" {
			n++
		}
	}
	return n
}
