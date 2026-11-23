package turnguards_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/project/projecttest"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testtool"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/toolstest"
	"github.com/lycaon/lycaon/pkg/api"
)

const reproducedSourceRuleID = "REPRODUCED_SOURCE_IN_ANSWER"

type scriptedTurnClient struct {
	mu          sync.Mutex
	completions []*modelcall.Completion
	requests    []modelcall.CompletionRequest
	idx         int
}

func (c *scriptedTurnClient) ModelEntry(_ string) (modelinfo.Entry, bool) {
	return modelinfo.Entry{}, false
}

func (c *scriptedTurnClient) Complete(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, req)
	if c.idx >= len(c.completions) {
		return &modelcall.Completion{Content: "done", FinishReason: "stop"}, nil
	}
	out := *c.completions[c.idx]
	c.idx++
	return &out, nil
}

func (c *scriptedTurnClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	comp, err := c.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{
		Content:      comp.Content,
		ToolCalls:    comp.ToolCalls,
		FinishReason: comp.FinishReason,
		Done:         true,
	}
	close(ch)
	return ch, nil
}

func (c *scriptedTurnClient) Requests() []modelcall.CompletionRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]modelcall.CompletionRequest, len(c.requests))
	copy(out, c.requests)
	return out
}

type reproducedSourceFixture struct {
	mgr     *session.Host
	store   *store.Repositories
	client  *scriptedTurnClient
	rootDir string
	projID  string
}

func loadStockGuardPipeline(t *testing.T) *oar.GuardPipeline {
	t.Helper()
	modRoot := configlayout.FindModuleRoot()
	loader, err := oar.NewLoader(filepath.Join(filepath.Dir(modRoot), "schemas"))
	testutil.FailErr(t, "create OAR loader", err)
	rules, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "load effective OAR policy", err)
	p := oar.NewGuardPipeline(rules, loader, oar.NewCounterStore())
	for _, anchor := range []string{
		oar.AnchorCoordinatorPreInvoke,
		oar.AnchorCoordinatorCloseoutCheck,
		oar.AnchorCoordinatorPostTurn,
		oar.AnchorToolPreInvoke,
		oar.AnchorWorkerReportCheck,
		oar.AnchorToolPost,
	} {
		p.EnableAnchor(anchor)
	}
	return p
}

func newReproducedSourceFixture(t *testing.T, completions []*modelcall.Completion) *reproducedSourceFixture {
	sessionFixture := storetest.New(t)

	t.Helper()
	t.Setenv("LYCAON_LLM_MOCK", "1")

	rootDir := t.TempDir()
	sessionStore := sessionFixture
	client := &scriptedTurnClient{completions: completions}

	registry := toolstest.NewRegistry()
	testutil.FailErr(t, "register command tool", registry.Register("command", func(_ context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
		cmd, _ := args["command"].(string)
		rel := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(cmd), "cat "))
		raw, err := os.ReadFile(filepath.Join(rootDir, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		payload, err := json.Marshal(map[string]any{"ok": true, "command": cmd, "tail": string(raw)})
		return string(payload), err
	}))

	mgr := session.NewHost(sessionStore, session.Models{Client: client, Limits: settings.DefaultSessionLimits()}, registry)
	mgr.SetDataDir(t.TempDir())

	projects := project.NewSQLRegistry(sessionFixture.DB())
	proj, err := projecttest.CreateWithRoot(t.Context(), projects, rootDir)
	testutil.FailErr(t, "create rooted project", err)
	mgr.SetProjectRegistry(projects)

	agents := orchestration.NewMemoryAgentRegistry()
	testutil.FailErr(t, "load agent registry", orchestration.LoadRequiredAgentRegistry(t.Context(), agents))
	mgr.Profiles.SetAgentRegistry(agents)

	oartest.InstallCloseoutPolicy(t, mgr)
	testutil.FailErr(t, "install anchor registry", mgr.Coordinator.Guidance.InstallAnchorRegistry())
	mgr.Coordinator.Guards.SetToolMetadata(testtool.RegistryInvoker{Registry: registry})
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot()}))
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))

	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load stock hints", err)
	rejectFmt := guidance.NewStaticRejectFormatter(hints)
	mgr.SetWorkflowHints(hints, nil)
	mgr.SetRejectFormatter(rejectFmt)
	mgr.SetOARPipeline(loadStockGuardPipeline(t), oar.NewRenderer(rejectFmt, nil))

	return &reproducedSourceFixture{mgr: mgr, store: sessionStore, client: client, rootDir: rootDir, projID: proj.ID}
}

func buildSourceFileLines(prefix string, n int) string {
	lines := make([]string, 0, n)
	lines = append(lines, "package sample")
	for i := 1; i < n; i++ {
		lines = append(lines, fmt.Sprintf("func %sLine%03d() int { return %d }", prefix, i, i))
	}
	return strings.Join(lines, "\n") + "\n"
}

func extractPolicyFeedbackByCode(t *testing.T, msgs []api.Message, code string) []guidance.PolicyFeedback {
	t.Helper()
	var matched []guidance.PolicyFeedback
	for _, msg := range msgs {
		for _, part := range msg.ContentParts {
			if part.Source != "policy_feedback" {
				continue
			}
			var fb guidance.PolicyFeedback
			testutil.FailErr(t, "unmarshal policy_feedback part", json.Unmarshal([]byte(part.Content), &fb))
			if fb.Code == code {
				matched = append(matched, fb)
			}
		}
	}
	return matched
}

// Coordinator pastes queue one nudge for the next turn; workers, new code, and mid-turn text do not.
func TestReproducedSourcePostTurnEndToEnd(t *testing.T) {
	source40 := buildSourceFileLines("Handler", 40)

	t.Run("coordinator_paste_delivers_answer_and_queues_single_nudge_for_next_turn", func(t *testing.T) {
		pastedAnswer := "```go\n" + strings.TrimSuffix(source40, "\n") + "\n```"
		fix := newReproducedSourceFixture(t, []*modelcall.Completion{
			{
				FinishReason: "tool_calls",
				ToolCalls:    []api.ToolCall{{ID: "call_cmd_1", Name: "command", Args: map[string]any{"command": "cat service.go"}}},
			},
			{FinishReason: "stop", Content: pastedAnswer},
			{
				FinishReason: "tool_calls",
				ToolCalls:    []api.ToolCall{{ID: "call_cmd_t2", Name: "command", Args: map[string]any{"command": "cat service.go"}}},
			},
			{FinishReason: "stop", Content: "Summarized the handler responsibilities."},
			{FinishReason: "stop", Content: "Answered third question."},
		})
		testutil.FailErr(t, "write 40-line workspace file", os.WriteFile(filepath.Join(fix.rootDir, "service.go"), []byte(source40), 0o600))

		sess, err := fix.store.Sessions.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, fix.projID)
		testutil.FailErr(t, "create coordinator session", err)

		resp1, err := fix.mgr.Submissions.Prompt(t.Context(), sess.ID, "show me service.go")
		testutil.FailErr(t, "turn 1 prompt", err)
		if resp1.MessageID == "" {
			t.Fatal("expected delivered message ID on turn 1")
		}
		if got := len(fix.client.Requests()); got != 2 {
			t.Fatalf("turn 1 LLM requests = %d, want 2 (answer delivered without retry)", got)
		}

		transcript1, err := fix.store.Transcript.GetMessages(t.Context(), sess.ID)
		testutil.FailErr(t, "read transcript after turn 1", err)
		nudges1 := extractPolicyFeedbackByCode(t, transcript1, reproducedSourceRuleID)
		if len(nudges1) != 1 {
			t.Fatalf("persisted nudges after turn 1 = %d, want 1", len(nudges1))
		}
		if nudges1[0].Effect != string(oar.EffectNudge) ||
			nudges1[0].Anchor != oar.AnchorCoordinatorPostTurn ||
			nudges1[0].Retain != api.PolicyFeedbackRetainNextTurn {
			t.Fatalf("nudge metadata = %+v", nudges1[0])
		}

		_, err = fix.mgr.Submissions.Prompt(t.Context(), sess.ID, "follow up with a tool step first")
		testutil.FailErr(t, "turn 2 prompt", err)

		reqs := fix.client.Requests()
		if len(reqs) != 4 {
			t.Fatalf("LLM requests after turn 2 = %d, want 4 (opening + post-tool final answer)", len(reqs))
		}
		if turn2Opening := extractPolicyFeedbackByCode(t, reqs[2].Messages, reproducedSourceRuleID); len(turn2Opening) != 1 {
			t.Fatalf("turn 2 opening request nudges = %d, want 1", len(turn2Opening))
		}
		if turn2Final := extractPolicyFeedbackByCode(t, reqs[3].Messages, reproducedSourceRuleID); len(turn2Final) != 1 {
			t.Fatalf("turn 2 post-tool final request nudges = %d, want 1", len(turn2Final))
		}
		transcript2, err := fix.store.Transcript.GetMessages(t.Context(), sess.ID)
		testutil.FailErr(t, "read transcript after turn 2", err)
		if nudges2 := extractPolicyFeedbackByCode(t, transcript2, reproducedSourceRuleID); len(nudges2) != 1 {
			t.Fatalf("persisted nudges after clean turn 2 = %d, want 1", len(nudges2))
		}

		_, err = fix.mgr.Submissions.Prompt(t.Context(), sess.ID, "third question")
		testutil.FailErr(t, "turn 3 prompt", err)
		reqs = fix.client.Requests()
		if len(reqs) != 5 {
			t.Fatalf("LLM requests after turn 3 = %d, want 5", len(reqs))
		}
		if turn3Delivered := extractPolicyFeedbackByCode(t, reqs[4].Messages, reproducedSourceRuleID); len(turn3Delivered) != 0 {
			t.Fatalf("turn 3 request nudges = %d, want 0 (retired after turn 2 final answer)", len(turn3Delivered))
		}
	})

	t.Run("worker_session_pasting_touched_file_produces_no_nudge", func(t *testing.T) {
		pastedAnswer := "```go\n" + strings.TrimSuffix(source40, "\n") + "\n```"
		fix := newReproducedSourceFixture(t, nil)
		testutil.FailErr(t, "write 40-line workspace file", os.WriteFile(filepath.Join(fix.rootDir, "service.go"), []byte(source40), 0o600))

		parent, err := fix.store.Sessions.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, fix.projID)
		testutil.FailErr(t, "create parent session", err)
		workerSess, err := fix.mgr.Workers.SpawnChild(t.Context(), parent.ID, api.SpawnChildRequest{
			AgentType: orchestration.ProfileImplementer,
			Prompt:    "inspect service.go",
		})
		testutil.FailErr(t, "spawn worker session", err)

		_, _, err = fix.store.Evidence.CommitEvidenceToolResult(t.Context(), workerSess.ID, fix.rootDir, "read", map[string]any{"path": "service.go"}, source40)
		testutil.FailErr(t, "commit worker read evidence", err)

		reject, blocked := fix.mgr.Coordinator.Guards.AfterTurn(t.Context(), workerSess, pastedAnswer, true)
		if blocked || reject != nil {
			t.Fatalf("worker AfterTurn blocked=%v reject=%v", blocked, reject)
		}
		staged, err := fix.mgr.Coordinator.Guidance.TakePolicy(t.Context(), workerSess.ID)
		testutil.FailErr(t, "take worker policy", err)
		if nudges := extractPolicyFeedbackByCode(t, staged, reproducedSourceRuleID); len(nudges) != 0 {
			t.Fatalf("worker session produced nudges: %+v", nudges)
		}
	})

	t.Run("coordinator_200_line_new_code_fence_produces_no_nudge", func(t *testing.T) {
		newCode200 := buildSourceFileLines("BrandNewSynthesized", 200)
		newCodeAnswer := "```go\n" + strings.TrimSuffix(newCode200, "\n") + "\n```"

		fix := newReproducedSourceFixture(t, []*modelcall.Completion{
			{
				FinishReason: "tool_calls",
				ToolCalls:    []api.ToolCall{{ID: "call_cmd_existing", Name: "command", Args: map[string]any{"command": "cat service.go"}}},
			},
			{FinishReason: "stop", Content: newCodeAnswer},
		})
		testutil.FailErr(t, "write 40-line workspace file", os.WriteFile(filepath.Join(fix.rootDir, "service.go"), []byte(source40), 0o600))

		sess, err := fix.store.Sessions.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, fix.projID)
		testutil.FailErr(t, "create coordinator session", err)

		_, err = fix.mgr.Submissions.Prompt(t.Context(), sess.ID, "write a 200-line example")
		testutil.FailErr(t, "prompt new code fence", err)
		if got := len(fix.client.Requests()); got != 2 {
			t.Fatalf("LLM requests = %d, want 2", got)
		}

		transcript, err := fix.store.Transcript.GetMessages(t.Context(), sess.ID)
		testutil.FailErr(t, "read transcript", err)
		if nudges := extractPolicyFeedbackByCode(t, transcript, reproducedSourceRuleID); len(nudges) != 0 {
			t.Fatalf("new code fence produced nudges: %+v", nudges)
		}
	})

	t.Run("mid_turn_undelivered_paste_before_clean_answer_produces_no_nudge", func(t *testing.T) {
		pastedMidTurn := "```go\n" + strings.TrimSuffix(source40, "\n") + "\n```"
		fix := newReproducedSourceFixture(t, []*modelcall.Completion{
			{
				FinishReason: "tool_calls",
				ToolCalls:    []api.ToolCall{{ID: "call_cmd_1", Name: "command", Args: map[string]any{"command": "cat service.go"}}},
			},
			{
				FinishReason: "tool_calls",
				Content:      pastedMidTurn,
				ToolCalls:    []api.ToolCall{{ID: "call_cmd_2", Name: "command", Args: map[string]any{"command": "cat service.go"}}},
			},
			{FinishReason: "stop", Content: "Inspected the handler implementation."},
		})
		testutil.FailErr(t, "write 40-line workspace file", os.WriteFile(filepath.Join(fix.rootDir, "service.go"), []byte(source40), 0o600))

		sess, err := fix.store.Sessions.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, fix.projID)
		testutil.FailErr(t, "create coordinator session", err)

		_, err = fix.mgr.Submissions.Prompt(t.Context(), sess.ID, "inspect service.go")
		testutil.FailErr(t, "prompt with mid-turn paste", err)
		if got := len(fix.client.Requests()); got != 3 {
			t.Fatalf("LLM requests = %d, want 3", got)
		}

		transcript, err := fix.store.Transcript.GetMessages(t.Context(), sess.ID)
		testutil.FailErr(t, "read transcript", err)
		if nudges := extractPolicyFeedbackByCode(t, transcript, reproducedSourceRuleID); len(nudges) != 0 {
			t.Fatalf("mid-turn text produced nudges: %+v", nudges)
		}
	})
}
