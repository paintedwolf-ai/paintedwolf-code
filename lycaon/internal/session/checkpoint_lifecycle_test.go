package session

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/secretmint"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// Orphan checkpoints retain file copies without a live session.
func TestRemoveOrphanCheckpointsKeepsLiveSessionsOnly(t *testing.T) {
	// Memory storage permits an orphan fixture; SQL rejects its publication.
	mgr, sessions := newTestManager(t)
	dir := t.TempDir()
	ctx := t.Context()
	live, err := sessions.Create(ctx, api.CreateSessionRequest{}, "project")
	testutil.FailErr(t, "create live session", err)
	liveSessionID := live.ID

	store := sessioncheckpoint.New(mgr.Workspace.DataDir, dir, mgr.Coordinator.Context.Sessions.(Store))
	_, err = store.Open(t.Context(), liveSessionID, "anchor-live")
	testutil.FailErr(t, "open live anchor", err)
	_, err = store.Open(t.Context(), "deleted-session", "anchor-dead")
	testutil.FailErr(t, "open orphan anchor", err)

	if removed := mgr.Chats.Captures.RemoveOrphanCheckpoints(ctx, dir); removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := store.Load(t.Context(), liveSessionID, "anchor-live"); err != nil {
		t.Fatalf("removed a checkpoint whose session is still live: %v", err)
	}
	if _, err := store.Load(t.Context(), "deleted-session", "anchor-dead"); !errors.Is(err, sessionstore.ErrCheckpointMissing) {
		t.Fatal("orphan checkpoint survived cleanup")
	}
}

func TestDeleteSessionRemovesCheckpoints(t *testing.T) {
	mgr, sessionID, dir := newCheckpointTestSession(t)
	checkpoint := sessioncheckpoint.New(mgr.Workspace.DataDir, dir, mgr.Coordinator.Context.Sessions.(Store))
	_, err := checkpoint.Open(t.Context(), sessionID, "anchor")
	testutil.FailErr(t, "open checkpoint", err)

	testutil.FailErr(t, "delete session", mgr.Chats.Delete(t.Context(), sessionID))
	if _, err := checkpoint.Load(t.Context(), sessionID, "anchor"); !errors.Is(err, sessionstore.ErrCheckpointMissing) {
		t.Fatalf("checkpoint survived session deletion: %v", err)
	}
}

func TestCompactionUsesObservedTokenOverhead(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.ModelContextWindow = 100000
	cfg.BudgetTriggerPct = 70
	cfg.HardCeilingTokens = 85000
	mgr, store := newCompactionManager(t, cfg)
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	block := strings.Repeat("tok ", 16000)
	transcript := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI([]api.Message{{Content: block}}))
	if transcript >= cfg.HardCeilingTokens {
		t.Fatalf("transcript = %d must stay below hard ceiling %d for fixture", transcript, cfg.HardCeilingTokens)
	}
	overhead := cfg.HardCeilingTokens - transcript + 1000
	mgr.Runner.History.ObserveTokens(sess.ID, sess.ProviderID, sess.Model, transcript+overhead, transcript)

	got := mgr.Runner.History.BudgetTokens(ctx, sess, transcript)
	if got < cfg.HardCeilingTokens {
		t.Fatalf("corrected budget = %d want >= hard ceiling %d", got, cfg.HardCeilingTokens)
	}
	if compaction.ShouldCompactSession(got, cfg.ModelContextWindow, cfg) {
		return
	}
	t.Fatalf("corrected budget %d should cross compaction trigger/ceiling", got)
}

// Cold-start estimates include the system prompt and tool schemas.
func TestCompactionColdStartReservesStaticStack(t *testing.T) {
	mgr, store := newCompactionManager(t, compaction.DefaultCompactionConfig())
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	block := strings.Repeat("x ", 1000)
	transcript := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI([]api.Message{{Content: block}}))
	got := mgr.Runner.History.BudgetTokens(ctx, sess, transcript)
	if got <= transcript {
		t.Fatalf("cold-start budget = %d, want above the transcript estimate %d", got, transcript)
	}
	want := transcript + compaction.ResolveColdStartOverhead("")
	if got != want {
		t.Fatalf("cold-start budget = %d want %d", got, want)
	}

	// Once an observation exists the ratio supersedes the flat reserve, and it
	// tracks growth instead of staying constant.
	mgr.Runner.History.ObserveTokens(sess.ID, sess.ProviderID, sess.Model, transcript*2, transcript)
	if scaled := mgr.Runner.History.BudgetTokens(ctx, sess, transcript); scaled != transcript*2 {
		t.Fatalf("calibrated budget = %d want %d", scaled, transcript*2)
	}
	if grown := mgr.Runner.History.BudgetTokens(ctx, sess, transcript*2); grown != transcript*4 {
		t.Fatalf("grown calibrated budget = %d want %d (scaled, not additive)", grown, transcript*4)
	}
}

func TestSessionContextUsesCorrectedEstimate(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.ModelContextWindow = 100000
	mgr, store := newCompactionManager(t, cfg)
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	block := strings.Repeat("a ", 2000)
	if err := store.AppendMessages(ctx, sess.ID,
		api.Message{ID: "u1", Role: api.MessageRoleUser, Content: block},
	); err != nil {
		t.Fatal(err)
	}
	transcript := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI([]api.Message{{Content: block}}))
	mgr.Runner.History.ObserveTokens(sess.ID, sess.ProviderID, sess.Model, transcript+15000, transcript)

	resp, err := mgr.Runner.History.SessionContext(ctx, sess.ID)
	testutil.FailErr(t, "SessionContext", err)
	want := compaction.PromptTokenCalibration{
		ReportedPromptTokens: transcript + 15000, TranscriptEstimate: transcript,
	}.ProjectBilled(transcript, 15000)
	if resp.EstimatedTokens < want {
		t.Fatalf("EstimatedTokens = %d want >= corrected %d", resp.EstimatedTokens, want)
	}
}

func TestCompactionFitThenReplace(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.HardCeilingTokens = 50000
	cfg.TargetTokens = 8000
	cfg.ChunkTokenThreshold = 500
	cfg.ChunkMinSavingsTokens = 100
	mgr, store := newCompactionManager(t, cfg)
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	log := strings.Repeat("ERROR: build failed line\n", 8000)
	if err := store.AppendMessages(ctx, sess.ID,
		api.Message{ID: "log", Role: api.MessageRoleUser, Content: log},
		api.Message{ID: "ack", Role: api.MessageRoleAssistant, Content: "seen"},
	); err != nil {
		t.Fatal(err)
	}
	transcript := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI([]api.Message{{Content: log}, {Content: "seen"}}))
	mgr.Runner.History.ObserveTokens(sess.ID, sess.ProviderID, sess.Model, transcript+25000, transcript)

	_, err = mgr.Submissions.Prompt(ctx, sess.ID, "what failed?")
	testutil.FailErr(t, "mgr.Submissions.Prompt", err)
	mgr.Runner.History.Wait()

	canonical, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "canonical GetMessages", err)
	if canonical[0].Content != log {
		t.Fatal("canonical history must stay full-fidelity after background pass")
	}

	sess, err = store.Get(ctx, sess.ID)
	testutil.FailErr(t, "store.Get after compaction", err)
	if sess.CompactionGeneration == 0 {
		t.Fatal("expected compaction generation bump after background pass")
	}
	view := appliedView(t, mgr, store, sess.ID)
	viewEst := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(view))
	if viewEst > cfg.TargetTokens {
		t.Fatalf("compacted view estimate = %d want <= target %d", viewEst, cfg.TargetTokens)
	}
}

func TestSettlementCannotCompleteWithOpenWorkflowGates(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(gatedCloseoutView{state: gatedExecuteState()}))
	hub := events.NewMemoryHub()
	mgr.SetEventPublisher(&events.Publisher{Hub: hub})
	eventsCh, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to settlement", err)
	t.Cleanup(unsubscribe)
	testutil.FailErr(t, "settle incomplete workflow", mgr.Runner.Settlement.Settle(t.Context(), sess.ID, api.SessionIdleDispositionCompleted))
	if got := awaitIdleDisposition(t, eventsCh); got != api.SessionIdleDispositionTurnError {
		t.Fatalf("disposition=%s", got)
	}
}

func TestCoordinatorBatchPhaseToolGuard_packBoardInSynthesize(t *testing.T) {
	sess := &api.Session{ID: "sess-1", Posture: api.SessionPostureBuild, AgentType: "coordinator"}
	gc := oar.NewGuardContext()
	guard.ObserveCoordinatorBatchPhaseTool(
		sess,
		"pack_board",
		surface.ImplementSessionState{BatchPhase: batch.PhaseSynthesize},
		guard.BatchTurnGuard{},
		gc,
	)
	if !guard.EvaluateObserveHasCode(gc, oar.AnchorCoordinatorPreInvoke, guard.CoordinatorBatchWrongPhaseCode) {
		t.Fatalf("want %s Decision for pack_board in synthesize", guard.CoordinatorBatchWrongPhaseCode)
	}
}

func TestQueueCoordinatorGuidanceNudgeUsesAdvisoryDisposition(t *testing.T) {
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)
	mgr, _ := newTestManager(t)
	mgr.SetRejectFormatter(guidance.NewStaticRejectFormatter(hints))
	mgr.Coordinator.Guidance.Queue(t.Context(), "sess-nudge", "COORDINATOR_HOST_TURN_REQUIRES_WAIT", nil, nil)
	if id := mgr.Coordinator.Runtime.Kicks().TakePendingKickID("sess-nudge"); id != "guidance:COORDINATOR_HOST_TURN_REQUIRES_WAIT:session:sess-nudge" {
		t.Fatalf("kick id = %q", id)
	}
	text, _, ok, _ := mgr.Coordinator.Runtime.Kicks().RenderPendingNudge(t.Context(), "sess-nudge", kick.CoordinatorKickRenderContext{})
	if !ok || strings.Contains(text, "Rejected:") || !strings.Contains(text, "Code: COORDINATOR_HOST_TURN_REQUIRES_WAIT") {
		t.Fatalf("nudge = %q ok=%v", text, ok)
	}
}

func TestStripHostBlocks(t *testing.T) {
	in := "Do work\n>>> Spec posture blocked\nRejected: X\nCode: SPEC_POSTURE_STUB_REQUIRED\nCall task(agent_type=foo)\n"
	got := guidance.StripHostBlocks(in)
	if strings.Contains(got, "Spec posture") || strings.Contains(got, "Rejected:") || strings.Contains(got, "Code: SPEC_") {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(got, "Do work") {
		t.Fatalf("got %q", got)
	}
	// Host-authored guidance remains intact.
	if !strings.Contains(got, "Call task(agent_type=foo)") {
		t.Fatalf("coordinator-authored line was dropped: %q", got)
	}
}

func TestCredentialAdvisoriesDistinguishWeakAndOtherLiterals(t *testing.T) {
	mgr, _ := newWeakSecretMintGuidanceManager(t)
	cases := []struct {
		name, tool, code string
		args             map[string]any
	}{
		{"nested account command", "command", "WEAK_CREDENTIAL_LITERAL", map[string]any{"command": "docker exec service gitea admin user create --password password"}},
		{"other credential literal", "command", "CREDENTIAL_LITERAL", map[string]any{"command": "example --client-secret=A123456789012345"}},
		{"structured tool credential", "external_tool", "WEAK_CREDENTIAL_LITERAL", map[string]any{"settings": map[string]any{"password": "password"}}},
		{"managed reference", "command", "", map[string]any{"command": "example --password={{paintedwolf-secret:123e4567-e89b-42d3-a456-426614174000}}"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			output, facts := mgr.ToolPolicy.AfterTool(t.Context(), &api.Session{ID: tc.name}, tc.tool, tc.args, "completed", 1, guidance.ToolResultFacts{})
			if !facts.Succeeded() {
				t.Fatalf("advisory changed outcome: %#v", facts)
			}
			if tc.code != "" && !facts.HasCode(tc.code) {
				t.Fatalf("missing %s: %s", tc.code, output)
			}
			if tc.code == "" && len(facts.Codes) > 0 {
				t.Fatalf("reference caused feedback: %#v", facts)
			}
		})
	}
}

func TestCredentialRemediesUseOfferedToolSnapshot(t *testing.T) {
	for _, offered := range []bool{false, true} {
		mgr, _ := newWeakSecretMintGuidanceManager(t)
		names := []string{}
		if offered {
			names = []string{"secret_generate", "ask_user"}
		}
		ctx := tools.WithRecoveryTools(t.Context(), names)
		output, facts := mgr.ToolPolicy.AfterTool(ctx, &api.Session{ID: "remedies"}, "command",
			map[string]any{"command": "example --password=password"}, "completed", 1, guidance.ToolResultFacts{})
		if !facts.HasCode("WEAK_CREDENTIAL_LITERAL") {
			t.Fatalf("credential advisory missing: %s", output)
		}
		for _, tool := range []string{"secret_generate", "ask_user"} {
			if strings.Contains(output, "`"+tool) != offered {
				t.Fatalf("remedy for %s disagrees with offered=%v: %s", tool, offered, output)
			}
		}
	}
}

func TestCredentialAdvisoriesKeepDistinctValueSubjects(t *testing.T) {
	mgr, _ := newWeakSecretMintGuidanceManager(t)
	_, facts := mgr.ToolPolicy.AfterTool(t.Context(), &api.Session{ID: "distinct-values"}, "write", map[string]any{"content": "MYSQL_PASSWORD=password\nPOSTGRES_PASSWORD=password1\n"}, "written", 1, guidance.ToolResultFacts{})
	result := guidance.ComposeToolResult("written", facts, nil)
	if len(result.Feedback) != 2 || result.Feedback[0].Subject == nil || result.Feedback[1].Subject == nil || result.Feedback[0].Subject.ID == result.Feedback[1].Subject.ID {
		t.Fatalf("credential subjects lost: %#v", result.Feedback)
	}
}

func TestCredentialRemediesDiscoverDeferredTools(t *testing.T) {
	for _, value := range []string{"password", "A123456789012345"} {
		for _, loaded := range []bool{false, true} {
			mgr, _ := newWeakSecretMintGuidanceManager(t)
			names := []string{"request_tools"}
			if loaded {
				names = append(names, "secret_generate", "ask_user")
			}
			ctx := tools.WithRecoveryTools(t.Context(), names)
			output, facts := mgr.ToolPolicy.AfterTool(ctx, &api.Session{ID: "deferred-remedies"}, "write",
				map[string]any{"content": "password=" + value}, "written", 1, guidance.ToolResultFacts{})
			if !facts.HasCode("WEAK_CREDENTIAL_LITERAL") && !facts.HasCode("CREDENTIAL_LITERAL") {
				t.Fatalf("credential observation lost: %s", output)
			}
			for _, tool := range []string{"secret_generate", "ask_user"} {
				if !strings.Contains(output, "`"+tool) {
					t.Fatalf("recoverable credential tool %s missing: %s", tool, output)
				}
			}
			if strings.Contains(output, "`request_tools`") == loaded {
				t.Fatalf("schema loading advice disagrees with loaded=%v: %s", loaded, output)
			}
			if strings.Contains(output, "report that need to the coordinating session") {
				t.Fatalf("reachable deferred tool was treated as unavailable: %s", output)
			}
		}
	}
}

func TestCredentialRecognitionReloadFeedsOAR(t *testing.T) {
	mgr, _ := newWeakSecretMintGuidanceManager(t)
	base, err := secretmint.LoadBundled()
	testutil.FailErr(t, "load baseline", err)
	unit, err := secretmint.ParseContribution([]byte("version: 1\nenv_keys: [ACCESS_VALUE]"))
	testutil.FailErr(t, "parse extension", err)
	extended, err := secretmint.Compile([]secretmint.Contribution{unit})
	testutil.FailErr(t, "compile extension", err)
	active := base
	calls := 0
	mgr.ToolPolicy.SetCredentialSlotProvider(func(context.Context, *api.Session) *secretmint.Inspector {
		calls++
		return active
	})
	for _, step := range []struct {
		name string
		ins  *secretmint.Inspector
		want bool
	}{
		{"before install", base, false},
		{"enabled", extended, true},
		{"removed", base, false},
	} {
		active = step.ins
		output, facts := mgr.ToolPolicy.AfterTool(t.Context(), &api.Session{ID: step.name}, "external_tool", map[string]any{"ACCESS_VALUE": "password"}, "completed", 0, guidance.ToolResultFacts{}.WithOutcome(api.ToolResultOutcomeCompleted))
		if facts.HasCode("WEAK_CREDENTIAL_LITERAL") != step.want || !facts.Succeeded() {
			t.Fatalf("%s: unexpected OAR feedback: %#v", step.name, facts)
		}
		if strings.Contains(output, "ACCESS_VALUE=password") {
			t.Fatal("credential value escaped into advisory")
		}
	}
	if calls != 3 {
		t.Fatalf("provider calls = %d; each invocation must select current recognition", calls)
	}
}
