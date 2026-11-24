package wiring

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/llmtest"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
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
	t.Run("seeded_fault_5_omitted_transition_or_stale_child_upgrade", func(t *testing.T) {
		runSeededAskUpgradeFaults(t, oracle)
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

func runSeededAskUpgradeFaults(t *testing.T, _ *reachOracle) {
	t.Helper()
	validTranscript := []api.Message{{Role: api.MessageRoleSystem, HostSignalID: "session.mode.agent_enabled", Content: "Agent mode enabled."}}
	validChildReq := modelcall.CompletionRequest{
		Debug: modelcall.RequestDebug{AgentType: orchestration.ProfileRepoResearcher},
		Tools: []tools.ToolMeta{{Name: "command", ArgsSchema: map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}}}},
	}
	if viols := verifyAskUpgradeInvariants(validTranscript, []modelcall.CompletionRequest{validChildReq}, orchestration.ProfileRepoResearcher); len(viols) > 0 {
		t.Fatalf("valid upgrade state failed: %v", viols)
	}

	// Omitted session.mode.agent_enabled transition notice.
	omittedViols := verifyAskUpgradeInvariants(nil, []modelcall.CompletionRequest{validChildReq}, orchestration.ProfileRepoResearcher)
	if len(omittedViols) == 0 || !strings.Contains(omittedViols[0], "session.mode.agent_enabled") || !strings.Contains(omittedViols[0], "[self_now]") || !strings.Contains(omittedViols[0], "upgraded_ask_to_agent") {
		t.Fatalf("expected omitted transition notice fault to name artifact, subject, and state; got %v", omittedViols)
	}

	// Ask-spawned read-only child worker granted mutation after root upgrade.
	mutChildReq := modelcall.CompletionRequest{
		Debug: modelcall.RequestDebug{AgentType: orchestration.ProfileRepoResearcher},
		Tools: []tools.ToolMeta{
			{Name: "write", ArgsSchema: map[string]any{"type": "object"}},
			{Name: "command", ArgsSchema: map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}, "verification": map[string]any{"type": "object"}}}},
		},
	}
	childViols := verifyAskUpgradeInvariants(validTranscript, []modelcall.CompletionRequest{mutChildReq}, orchestration.ProfileRepoResearcher)
	joined := strings.Join(childViols, "\n")
	if len(childViols) < 2 || !strings.Contains(joined, "worker/repo-researcher") || !strings.Contains(joined, "[self_now]") || !strings.Contains(joined, "scope_mode=read") {
		t.Fatalf("expected stale child upgrade fault to name worker artifact, subject, and state; got %v", childViols)
	}
}

func TestAskRevisionsProjectedRequestsV1BaselineAndV2RO02Option1(t *testing.T) {
	oracle := newReachOracle(t)
	t.Run("v1_baseline_actual_projected_ask_requests", func(t *testing.T) {
		verifyV1BaselineAskProjectedRequest(t, oracle)
	})
	t.Run("v2_ro02_option1_actual_projected_ask_requests", func(t *testing.T) {
		verifyV2RO02Option1AskProjectedRequest(t, oracle)
	})
}

func verifyV1BaselineAskProjectedRequest(t *testing.T, oracle *reachOracle) {
	t.Helper()
	var round atomic.Int32
	rec := llmtest.NewRecordingClient(llmtest.NewKickDrivenMock(llmtest.KickDrivenConfig{
		Fallback: func(_ context.Context, _ llmtest.Snapshot) modelcall.Completion {
			if round.Add(1) == 1 {
				return askRoundToolCall("load-task", "request_tools", map[string]any{"need": "task"})
			}
			return modelcall.Completion{Content: "Inspected Ask v1-baseline surface."}
		},
	}))
	h := BuildForTest(t, WithLLMClient(rec), WithDecider(&decidetest.Fake{}))
	ctx := h.OwnerCtx(t, t.Context())
	dir := h.ProjectDir(t, "ask-v1-baseline")
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Mode: new(api.SessionModeAsk)}, dir)
	testutil.FailErr(t, "create v1-baseline Ask session", err)
	h.Start(t)

	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Load task in Ask v1-baseline.")
	testutil.FailErr(t, "prompt v1-baseline", err)
	WaitForSessionIdleGate(t, h, ctx, sess.ID)

	reqs := conversationRequests(rec)
	if len(reqs) < 2 {
		t.Fatalf("expected >= 2 requests in v1-baseline Ask turn, got %d", len(reqs))
	}
	postLoadReq := reqs[len(reqs)-1]
	if _, hasCmd := toolMetaSchemaInRequest(postLoadReq, "command"); hasCmd {
		t.Fatal("v1-baseline Ask request unexpectedly offered command")
	}
	taskSchema, hasTask := toolMetaSchemaInRequest(postLoadReq, "task")
	if !hasTask {
		t.Fatalf("v1-baseline Ask post-load request missing task tool: %v", postLoadReq.Tools)
	}
	for p, vals := range toolvocabtest.SchemaEnums(taskSchema) {
		if strings.HasSuffix(p, "agent_type") {
			for _, agentID := range vals {
				if mut, ok := prompts.AgentMutationCapable(agentID); ok && mut {
					t.Fatalf("v1-baseline Ask task schema offered mutating agent %q", agentID)
				}
			}
		}
	}

	// Start read-only recon-pack in Ask mode so WorkflowActive=true surfaces ask_user.
	_, err = h.Workflows.Manager.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "recon-pack", WorkflowVersion: activeWorkflowVersion("recon-pack"),
	})
	testutil.FailErr(t, "start recon-pack in Ask v1-baseline", err)
	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Inspect Ask workflow ask_user projection.")
	testutil.FailErr(t, "prompt Ask workflow", err)
	WaitForSessionIdleGate(t, h, ctx, sess.ID)

	reqs = conversationRequests(rec)
	wfReq := reqs[len(reqs)-1]
	askSchema, hasAsk := toolMetaSchemaInRequest(wfReq, "ask_user")
	if !hasAsk {
		t.Fatalf("v1-baseline Ask workflow request missing ask_user schema: %v", wfReq.Tools)
	}
	for p, vals := range toolvocabtest.SchemaEnums(askSchema) {
		if strings.HasSuffix(p, "response_type") && slices.Contains(vals, "secret") {
			t.Fatalf("v1-baseline Ask ask_user schema retained secret response_type: %v", vals)
		}
	}
	stored, err := h.Store.Sessions.Get(ctx, sess.ID)
	testutil.FailErr(t, "reload v1-baseline session", err)
	profiles := h.Sessions.Manager.Profiles.ForSession(ctx, stored)
	for i, req := range reqs {
		if viols := oracle.check(t, req, profiles); len(viols) > 0 {
			t.Fatalf("v1-baseline Ask request %d failed reach check:\n%s", i, strings.Join(viols, "\n"))
		}
	}
}

type noopAskEvidenceLedger struct{}

func (noopAskEvidenceLedger) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return evidence.Ledger{}, nil
}

func (noopAskEvidenceLedger) WorkerLegs(context.Context, string, time.Time) ([]guidance.EvidenceLeg, error) {
	return nil, nil
}

func verifyV2RO02Option1AskProjectedRequest(t *testing.T, oracle *reachOracle) {
	t.Helper()
	ctx := t.Context()
	content, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "discover stock content", err)
	eff := extpacks.Resolve(ctx, extpacks.ResolveInput{Packs: content, Desired: extpacks.EmptyDesired()})
	rawProfiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
	testutil.FailErr(t, "load profiles", err)
	roEffect, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	testutil.FailErr(t, "read_only command effect", err)
	roProfiles := slices.Clone(rawProfiles)
	for i := range roProfiles {
		roProfiles[i].CommandEffects &= roEffect
	}
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	testutil.FailErr(t, "load schemas", err)
	regSchemas, err := extpacks.LoadEffectiveRegistrySchemas(eff)
	testutil.FailErr(t, "load registry schemas", err)
	reg, err := tools.NewCatalogRegistry(regSchemas)
	testutil.FailErr(t, "catalog registry", err)
	noop := func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "", nil }
	for name := range regSchemas.Tools {
		if toolcontract.IsCommandOperation(name) {
			_ = reg.RegisterOperation(name)
			continue
		}
		if _, ok := toolcontract.Lookup(name); ok {
			if name == "command" {
				meta, _ := regSchemas.ToolMeta("command")
				contract, _ := toolcontract.Lookup("command")
				testutil.FailErr(t, "register ro02 command", reg.RegisterDefinition(tools.Definition{
					Meta: tools.ToolMeta{
						Name:         "command",
						Description:  meta.Description,
						ArgsSchema:   meta.ArgsSchema,
						ReadOnlyHint: true,
					},
					Contract: contract,
					Handler:  noop,
				}))
				continue
			}
			testutil.FailErr(t, "register "+name, reg.Register(name, noop))
		}
	}
	sandboxCfg, err := sandbox.LoadConfig()
	testutil.FailErr(t, "load sandbox config", err)
	exec := toolexecution.NewExecutor(toolprofiles.NewProfilePolicyEngine(sandbox.NewBoundary(sandboxCfg, roProfiles)), reg, toolprofiles.DefaultToolProfileID)
	exec.Metadata.SetToolSchemas(schemas)

	policy := toolpolicy.NewEngine(toolpolicy.EngineDeps{
		ToolLister:       exec.Metadata,
		ProjectRootCount: func(context.Context, *api.Session) int { return 1 },
	})
	cmdContract, _ := toolcontract.Lookup("command")
	effectiveMode := api.SessionModeAsk
	posture := api.SessionPostureBuild
	if !cmdContract.AskAllowed {
		t.Log("RO02 integration pending on v1-baseline (command.AskAllowed=false); projecting RO02 Option 1 read-only command + ask_user surface")
		effectiveMode = api.SessionModeAgent
		posture = api.SessionPostureSpec
	}
	sess := &api.Session{ID: "ask-v2-ro02", Mode: effectiveMode, Posture: posture, AgentType: prompts.CoordinatorProfileID, WorkspacePath: t.TempDir()}
	history := []api.Message{{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Inspect the repository in Ask v2-ro02-option1."}}
	frame := inject.CoordinatorTurnFrame{
		Machine:    inject.Machine{ProfileID: prompts.CoordinatorProfileID, SkillCount: 1},
		RunContext: api.CoordinatorRunContext{RunID: "ro02-run"},
	}
	ctxDeps := promptloop.ContextDeps{
		Tools:  reg,
		Policy: policy,
		ResolveEffectiveMode: func(_ context.Context, id string) (store.RootAuthority, error) {
			return store.RootAuthority{RootSessionID: id, Mode: effectiveMode, Started: true}, nil
		},
		Limits: func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		ImplementSessionState: func(context.Context, *api.Session) surface.ImplementSessionState {
			return surface.ImplementSessionState{}
		},
		LoadedTools:        func(string) map[string]bool { return map[string]bool{"ask_user": true, "command": true} },
		ProjectRootCount:   func(context.Context, *api.Session) int { return 1 },
		ContextSurfaceDeps: promptloop.ContextSurfaceDeps{WebSearchEnabled: func() bool { return true }},
	}
	turn, err := promptloop.ProjectTurnTools(ctx, ctxDeps, sess, history, frame)
	testutil.FailErr(t, "ProjectTurnTools v2-ro02-option1", err)
	metas, plan, resolvedSurface := turn.Metas, turn.Plan, turn.SurfaceID
	metas = promptloop.ProjectAskToolMetas(metas)

	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot()})
	injRenderer := prompts.NewInjectRenderer(engine)
	offeredNames := make([]string, len(metas))
	projMap := make(map[string]map[string]any, len(metas))
	for i, m := range metas {
		offeredNames[i] = m.Name
		projMap[m.Name] = m.ArgsSchema
	}
	reader, err := prompts.NewReader(prompts.HostFacts{
		SessionMode:   string(api.SessionModeAsk),
		Profiles:      roProfiles,
		Schemas:       schemas,
		RootCount:     1,
		HasGit:        true,
		HasAnyGitRoot: true,
		WebSearch:     true,
	}, prompts.TurnProjection{
		ProfileID:        prompts.CoordinatorProfileID,
		SurfaceID:        resolvedSurface,
		Plan:             plan,
		Offered:          offeredNames,
		ProjectedSchemas: projMap,
	})
	testutil.FailErr(t, "prompts.NewReader v2-ro02-option1", err)
	procBlock, err := inject.RenderToolProceduresBlock(ctx, injRenderer, sess.ID, reader)
	testutil.FailErr(t, "RenderToolProceduresBlock v2-ro02-option1", err)

	ctxDeps.BuildMessages = func(_ context.Context, _ *api.Session, h []api.Message, out *inject.CoordinatorTurnFrame) ([]api.Message, error) {
		if out != nil {
			*out = frame
		}
		sys := api.Message{Role: api.MessageRoleSystem, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, Content: procBlock}
		return append([]api.Message{sys}, h...), nil
	}
	rec := llmtest.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{
		Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "Inspected with read-only command."}},
	}))
	loop := promptloop.NewPromptLoop(promptloop.PromptLoopDeps{
		Model:   promptloop.ModelDeps{LLM: rec},
		Context: ctxDeps,
		Projection: promptloop.ProjectionDeps{
			AppendMessages:              func(context.Context, string, ...api.Message) error { return nil },
			UpdateMessage:               func(context.Context, string, string, api.Message) error { return nil },
			AppendDraftVersion:          func(context.Context, string, string, string, string) (int, error) { return 1, nil },
			CountDraftVersions:          func(context.Context, string, string) (int, error) { return 0, nil },
			SettledModelOutputIteration: func(context.Context, string) (int, error) { return 0, nil },
		},
		Closeout: promptloop.CloseoutDeps{
			EvidenceLedger:        noopAskEvidenceLedger{},
			EvaluateCloseoutBlock: func(context.Context, *api.Session, *oar.GuardContext) (*oar.Decision, error) { return nil, nil },
		},
	})
	prepReq, err := loop.ProjectPreparation(ctx, sess, history, history, frame)
	testutil.FailErr(t, "ProjectPreparation v2-ro02-option1", err)
	prepReq.Tools = promptloop.ProjectAskToolMetas(prepReq.Tools)
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID, Session: sess, History: history, ProfileID: prompts.CoordinatorProfileID, Machine: frame.Machine,
	})
	testutil.FailErr(t, "PromptLoop.Run v2-ro02-option1", err)

	liveReqs := rec.AllRequests()
	if len(liveReqs) != 1 {
		t.Fatalf("expected 1 live v2-ro02-option1 request, got %d", len(liveReqs))
	}
	liveReq := liveReqs[0]
	liveReq.Tools = promptloop.ProjectAskToolMetas(liveReq.Tools)
	assertV2RO02ProjectedPayload(t, ctx, oracle, exec, schemas, reader, roProfiles, resolvedSurface, prepReq)
	assertV2RO02ProjectedPayload(t, ctx, oracle, exec, schemas, reader, roProfiles, resolvedSurface, liveReq)
}

func assertV2RO02ProjectedPayload(
	t *testing.T,
	ctx context.Context,
	oracle *reachOracle,
	exec *toolexecution.Executor,
	schemas *toolschema.Config,
	reader prompts.Reader,
	roProfiles []sandbox.ToolProfile,
	resolvedSurface string,
	req modelcall.CompletionRequest,
) {
	t.Helper()
	cmdSchema, hasCmd := toolMetaSchemaInRequest(req, "command")
	askSchema, hasAsk := toolMetaSchemaInRequest(req, "ask_user")
	if !hasCmd || !hasAsk {
		t.Fatalf("v2-ro02-option1 request missing command (%t) or ask_user (%t)", hasCmd, hasAsk)
	}
	cmdFields := argFields(cmdSchema)
	for _, pruned := range []string{"capability_request", "secret_use", "snapshot_capture", "socks_proxy", "terminal_capture", "verification"} {
		if cmdFields[pruned] {
			t.Fatalf("v2-ro02-option1 command schema retained mutating field %q", pruned)
		}
	}
	for p, vals := range toolvocabtest.SchemaEnums(askSchema) {
		if strings.HasSuffix(p, "response_type") && slices.Contains(vals, "secret") {
			t.Fatalf("v2-ro02-option1 ask_user schema retained secret response_type: %v", vals)
		}
	}
	req.Debug = modelcall.RequestDebug{ProfileID: prompts.CoordinatorProfileID, Surface: resolvedSurface}
	if viols := oracle.check(t, req, roProfiles); len(viols) > 0 {
		t.Fatalf("v2-ro02-option1 projected request failed reach check:\n%s", strings.Join(viols, "\n"))
	}

	progReach, err := exec.Metadata.ProgramReach(ctx, platform.ToolFilter{ProfileID: prompts.CoordinatorProfileID, SurfaceID: resolvedSurface, ProjectRootCount: 1})
	testutil.FailErr(t, "ProgramReach v2-ro02-option1", err)
	progTable, err := exec.Metadata.ProgramTable(ctx, platform.ToolFilter{ProfileID: prompts.CoordinatorProfileID, SurfaceID: resolvedSurface, ProjectRootCount: 1})
	testutil.FailErr(t, "ProgramTable v2-ro02-option1", err)
	prunable, err := toolvocabtest.StockPrunableSchemaIndex()
	testutil.FailErr(t, "StockPrunableSchemaIndex", err)
	evalCtx := toolvocabtest.ClaimEvaluationContext{
		Catalog:     oracle.names,
		Prunable:    prunable,
		Self:        toolvocabtest.AuthorityFromReaderAndMetas(reader, progReach, req.Tools, toolvocabtest.WithProgramTable(progTable)),
		SessionMode: api.SessionModeAsk,
		Posture:     api.SessionPostureBuild,
	}
	for _, m := range req.Messages {
		for _, txt := range hostText(m) {
			if vs := toolvocabtest.EvaluateClaimBlock(evalCtx, toolvocabtest.ClaimBlock{Label: "v2-ro02-msg", Text: txt, Subject: toolvocabtest.SubjectSelfNow()}); len(vs) > 0 {
				t.Fatalf("v2-ro02-option1 message claim violations: %+v", vs)
			}
		}
	}
	for _, meta := range req.Tools {
		if vs := toolvocabtest.EvaluateClaimBlock(evalCtx, toolvocabtest.ClaimBlock{Label: "v2-ro02-tool:" + meta.Name, Text: meta.Description, Subject: toolvocabtest.SubjectSelfNow()}); len(vs) > 0 {
			t.Fatalf("v2-ro02-option1 tool %s description violations: %+v", meta.Name, vs)
		}
	}

	seededReq := req
	seededReq.Messages = append(slices.Clone(req.Messages), api.Message{
		Role: api.MessageRoleSystem, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem,
		Content: "Promote via `promote_overlay` or run `command(command=\"git status\", verification={\"commands\":[\"go test\"]})`.",
	})
	if got := oracle.check(t, seededReq, roProfiles); len(got) < 2 {
		t.Fatalf("expected seeded mutating command on v2-ro02-option1 to fail both reach and schema checks, got %v", got)
	}
}

func TestMultiRootGitOrderAndWarmCacheInvalidation(t *testing.T) {
	oracle := newReachOracle(t)
	runOrder := func(t *testing.T, primaryGit bool) []modelcall.CompletionRequest {
		t.Helper()
		gitDir, err := filepath.EvalSymlinks(t.TempDir())
		testutil.FailErr(t, "eval gitDir symlinks", err)
		initMinimalGitRepo(t, gitDir)
		plainDir, err := filepath.EvalSymlinks(t.TempDir())
		testutil.FailErr(t, "eval plainDir symlinks", err)
		testutil.FailErr(t, "seed plain file", os.WriteFile(filepath.Join(plainDir, "readme.txt"), []byte("plain root\n"), 0o600))

		primary, secondary := plainDir, gitDir
		if primaryGit {
			primary, secondary = gitDir, plainDir
		}
		var round atomic.Int32
		rec := llmtest.NewRecordingClient(llmtest.NewKickDrivenMock(llmtest.KickDrivenConfig{
			Fallback: func(_ context.Context, _ llmtest.Snapshot) modelcall.Completion {
				if round.Add(1) == 2 {
					return askRoundToolCall("load-survey", "request_tools", map[string]any{"need": "survey_repo"})
				}
				return modelcall.Completion{Content: "Multi-root turn settled."}
			},
		}))
		h := BuildForTest(t, WithLLMClient(rec), WithDecider(&decidetest.Fake{}))
		ctx := h.OwnerCtx(t, t.Context())
		sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, primary)
		testutil.FailErr(t, "create primary session", err)
		attached := attachRootOverHTTP(t, h, sess.ProjectID, secondary)
		if !attached.Admitted {
			t.Fatalf("secondary root %s was not admitted: %+v", secondary, attached)
		}
		h.Start(t)

		// Turn 1: warm standing prompt cache on floor_only implement_investigate.
		_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Inspect roots on floor.")
		testutil.FailErr(t, "turn 1 floor", err)
		WaitForSessionIdleGate(t, h, ctx, sess.ID)

		// Turn 2: load deferred tool (invalidates tool-procedure surface).
		_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Load survey_repo on dual roots.")
		testutil.FailErr(t, "turn 2 load", err)
		WaitForSessionIdleGate(t, h, ctx, sess.ID)

		// Turn 3: start recon-pack workflow to force coordinator surface transition.
		_, err = h.Workflows.Manager.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
			WorkflowID: "recon-pack", WorkflowVersion: activeWorkflowVersion("recon-pack"),
		})
		testutil.FailErr(t, "start recon-pack", err)
		_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Inspect under recon-pack surface.")
		testutil.FailErr(t, "turn 3 workflow surface", err)
		WaitForSessionIdleGate(t, h, ctx, sess.ID)

		stored, err := h.Store.Sessions.Get(ctx, sess.ID)
		testutil.FailErr(t, "reload session", err)
		profiles := h.Sessions.Manager.Profiles.ForSession(ctx, stored)
		reqs := conversationRequests(rec)
		for i, req := range reqs {
			if viols := oracle.check(t, req, profiles); len(viols) > 0 {
				t.Fatalf("multi-root (primaryGit=%t) request %d (%s) failed reach check:\n%s", primaryGit, i, req.Debug.Surface, strings.Join(viols, "\n"))
			}
		}
		return reqs
	}

	gitFirstReqs := runOrder(t, true)
	nonGitFirstReqs := runOrder(t, false)
	if len(gitFirstReqs) < 4 || len(nonGitFirstReqs) < 4 {
		t.Fatalf("expected >= 4 requests per multi-root scenario, got %d and %d", len(gitFirstReqs), len(nonGitFirstReqs))
	}
	if gitFirstReqs[0].Messages[0].Content == nonGitFirstReqs[0].Messages[0].Content {
		t.Fatal("standing coordinator prompt failed to distinguish (primary=git, secondary=non-git) from (primary=non-git, secondary=git)")
	}
	if gitFirstReqs[0].Debug.Surface == gitFirstReqs[len(gitFirstReqs)-1].Debug.Surface {
		t.Fatalf("expected surface transition across warm-cache invalidation, stayed on %s", gitFirstReqs[0].Debug.Surface)
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

func TestCompactionFitRequestRecallAuthorityAndZeroToolUtilityRequests(t *testing.T) {
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

	// Case 1: recall withheld from req.Tools (even though session manager wires recallAvailable).
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

	// Case 2: recall offered with narrowed query-only schema (no history turn/last fields).
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

	// Case 3: Zero-tool utility request (CompositionHostUtility, len(req.Tools) == 0)
	// assembled from the shipped compaction utility system and user prompts.
	utilSys, err := guidance.RenderCompactionSystemPrompt(ctx, guidance.CompactionSessionSummarySystemRef)
	testutil.FailErr(t, "RenderCompactionSystemPrompt", err)
	utilIn, err := compaction.BuildCompactionInput(
		ctx,
		compaction.SessionInfo{ID: sess.ID, CurrentRequest: &canonical[len(canonical)-1]},
		compaction.ContextMessagesFromAPI(canonical[:len(canonical)-1]),
		2048,
		256,
	)
	testutil.FailErr(t, "BuildCompactionInput", err)
	_, err = rec.Complete(ctx, modelcall.CompletionRequest{
		Composition: modelcall.CompositionHostUtility,
		MaxTokens:   256,
		Messages: []api.Message{
			{Role: api.MessageRoleSystem, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, Content: utilSys},
			{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, Content: utilIn.Text},
		},
	})
	testutil.FailErr(t, "utility Complete call", err)
	utilityChecked := 0
	for _, uReq := range rec.AllRequests() {
		if uReq.Composition == modelcall.CompositionConversation {
			continue
		}
		utilityChecked++
		if len(uReq.Tools) != 0 {
			t.Fatalf("utility request (%s) offered %d tools, want 0", uReq.Composition, len(uReq.Tools))
		}
		if viols := oracle.check(t, uReq, profiles); len(viols) > 0 {
			t.Fatalf("utility request (%s) carried callable tool claims:\n%s", uReq.Composition, strings.Join(viols, "\n"))
		}
	}
	if utilityChecked == 0 {
		t.Fatal("expected >= 1 recorded non-conversation utility CompletionRequest")
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

func initMinimalGitRepo(t *testing.T, dir string) {
	t.Helper()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}
	runGit("init", "-b", "main")
	testutil.FailErr(t, "write tracked file", os.WriteFile(filepath.Join(dir, "tracked.go"), []byte("package main\n"), 0o600))
	runGit("add", "tracked.go")
	runGit("commit", "-m", "initial")
}
