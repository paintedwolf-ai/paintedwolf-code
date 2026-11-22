package wiring

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAskRoundUpgradeEnablesNativeWriteAndRewind(t *testing.T) {
	var h *Harness
	var id string
	var round atomic.Int32
	mock := llm.NewRecordingClient(llm.NewKickDrivenMock(llm.KickDrivenConfig{
		Fallback: func(ctx context.Context, _ llm.Snapshot) modelcall.Completion {
			switch round.Add(1) {
			case 1:
				// Commit the upgrade in the store while round 1's Ask response is in flight.
				// Round 1 must still execute its tool calls under its frozen Ask authority.
				if _, err := h.Store.SetMode(ctx, id, api.SessionModeAgent); err != nil {
					t.Errorf("commit live mode upgrade: %v", err)
				}
				return modelcall.Completion{ToolCalls: []api.ToolCall{
					{ID: "read", Name: "command", Args: map[string]any{"command": "cat note.txt"}},
					{ID: "blocked-write", Name: "command", Args: map[string]any{"command": "printf 'escaped\\n' > escaped.txt"}},
					{ID: "blocked-external", Name: "command", Args: map[string]any{"command": "/bin/echo outside"}},
				}}
			case 2:
				return askRoundToolCall("write", "command", map[string]any{"command": "printf 'created\n' > result.txt"})
			case 3:
				return askRoundToolCall("progress", "update_progress", h.ProgressUpdateArgs(t, ctx, id, "## Progress\n- [x] wiring test plan\n"))
			default:
				return modelcall.Completion{Content: MockCoordinatorCloseoutMarkdown("Wrote the requested result.", "")}
			}
		},
	}))
	h = BuildForTest(t, WithLLMClient(mock), WithDecider(decide.Absent{}))
	ctx := h.OwnerCtx(t, t.Context())
	dir := h.ProjectDir(t, "ask-upgrade")
	writeTestProjectApprovalsAllowWrite(t, dir)
	testutil.FailErr(t, "seed project source", os.WriteFile(filepath.Join(dir, "note.txt"), []byte("source evidence\n"), 0o600))
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Mode: new(api.SessionModeAsk)}, dir)
	testutil.FailErr(t, "create Ask session", err)
	id = sess.ID
	h.SeedProgress(t, ctx, id)
	cancel := h.StartBackgroundWorkers(t, ctx)
	t.Cleanup(cancel)

	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, id, "Read the note, then write a result if Agent mode is available.")
	testutil.FailErr(t, "run first prompt", err)
	assertAskUpgradeRounds(t, mock.AllRequests())
	if _, err := os.Stat(filepath.Join(dir, "escaped.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("in-flight Ask round wrote escaped.txt after store upgrade: %v", err)
	}
	resultPath := filepath.Join(dir, "result.txt")
	content, err := os.ReadFile(resultPath)
	testutil.FailErr(t, "read round-two result", err)
	if string(content) != "created\n" {
		t.Fatalf("round-two write = %q", content)
	}
	messages, err := h.Store.GetMessages(ctx, id)
	testutil.FailErr(t, "load transcript", err)
	verifyAskUpgradeTranscript(t, messages)
	anchor := visibleUserMessageIDs(t, messages)[0]
	WaitForSessionIdleGate(t, h, ctx, id)
	_, err = h.Sessions.Manager.Chats.Rewinds.RewindToPrompt(ctx, uuid.NewString(), id, anchor, rewindDigest(t, h.Sessions.Manager, ctx, id, anchor))
	testutil.FailErr(t, "rewind first turn", err)
	if _, err := os.Stat(resultPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("round-two file survived rewind: %v", err)
	}
	authority, err := h.Store.ResolveEffectiveMode(ctx, id)
	testutil.FailErr(t, "read mode after rewind", err)
	if authority.Mode != api.SessionModeAgent || !authority.Started {
		t.Fatalf("rewind changed session authority: %+v", authority)
	}
}

func askRoundToolCall(id, name string, args map[string]any) modelcall.Completion {
	return modelcall.Completion{ToolCalls: []api.ToolCall{{ID: id, Name: name, Args: args}}}
}

func assertAskUpgradeRounds(t *testing.T, requests []modelcall.CompletionRequest) {
	t.Helper()
	if len(requests) < 2 {
		t.Fatalf("model requests = %d", len(requests))
	}
	findTool := func(request modelcall.CompletionRequest, name string) (tools.ToolMeta, bool) {
		idx := slices.IndexFunc(request.Tools, func(tool tools.ToolMeta) bool { return tool.Name == name })
		if idx < 0 {
			return tools.ToolMeta{}, false
		}
		return request.Tools[idx], true
	}
	round1Cmd, ok1 := findTool(requests[0], "command")
	round2Cmd, ok2 := findTool(requests[1], "command")
	if !ok1 || !ok2 {
		t.Fatalf("command offer missing: round1=%t round2=%t", ok1, ok2)
	}
	if !slices.Equal(round1Cmd.DescriptionParts, []string{"base"}) {
		t.Fatalf("round 1 Ask command DescriptionParts = %v, want [base]", round1Cmd.DescriptionParts)
	}
	round1Props, _ := round1Cmd.ArgsSchema["properties"].(map[string]any)
	for _, pruned := range []string{"capability_request", "secret_use", "snapshot_capture", "socks_proxy", "terminal_capture", "verification"} {
		if _, present := round1Props[pruned]; present {
			t.Fatalf("round 1 Ask command schema still includes pruned field %q", pruned)
		}
	}
	round1Standing := requestStandingText(requests[0])
	if !strings.Contains(round1Standing, "## Ask mode") || !strings.Contains(round1Standing, "let the person decide whether to switch to Agent mode") {
		t.Fatalf("round 1 Ask standing context missing Ask mode orientation:\n%s", round1Standing)
	}
	if requestHasTransitionNotice(requests[0]) {
		t.Fatal("round 1 Ask request unexpectedly included an Agent transition notice")
	}

	if !slices.Contains(round2Cmd.DescriptionParts, "mutation") {
		t.Fatalf("round 2 Agent command DescriptionParts = %v, expected mutation", round2Cmd.DescriptionParts)
	}
	round2Props, _ := round2Cmd.ArgsSchema["properties"].(map[string]any)
	for _, restored := range []string{"capability_request", "verification"} {
		if _, present := round2Props[restored]; !present {
			t.Fatalf("round 2 Agent command schema missing restored field %q", restored)
		}
	}
	round2Standing := requestStandingText(requests[1])
	if strings.Contains(round2Standing, "## Ask mode") {
		t.Fatalf("round 2 Agent standing context still carried Ask mode section:\n%s", round2Standing)
	}
	if !requestHasTransitionNotice(requests[1]) {
		t.Fatal("round 2 Agent request missing Agent mode transition notice")
	}
	transitionText := requestTransitionNoticeText(requests[1])
	for _, want := range []string{
		"switched this session from Ask mode to Agent mode",
		"Earlier Ask-mode refusals applied only to calls evaluated while Ask mode was active.",
		"New calls in this round use the current Agent tool offer and ordinary permissions",
		"keeps its launch-time read-only authority",
		"does not automatically replay earlier calls or guarantee that a previously refused operation will succeed",
	} {
		if !strings.Contains(transitionText, want) {
			t.Fatalf("round 2 transition notice missing %q:\n%s", want, transitionText)
		}
	}
}

func requestTransitionNoticeText(req modelcall.CompletionRequest) string {
	for _, msg := range req.Messages {
		if msg.HostSignalID == "session.mode.agent_enabled" || strings.Contains(msg.Content, "[host:session-agent-enabled]") {
			return msg.Content
		}
	}
	return ""
}

func requestStandingText(req modelcall.CompletionRequest) string {
	var b strings.Builder
	for _, msg := range req.Messages {
		if msg.Role == api.MessageRoleSystem {
			b.WriteString(msg.Content)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func requestHasTransitionNotice(req modelcall.CompletionRequest) bool {
	for _, msg := range req.Messages {
		if msg.HostSignalID == "session.mode.agent_enabled" || strings.Contains(msg.Content, "[host:session-agent-enabled]") {
			return true
		}
	}
	return false
}

func countRequestTransitionNotices(req modelcall.CompletionRequest) int {
	n := 0
	for _, msg := range req.Messages {
		if msg.HostSignalID == "session.mode.agent_enabled" || strings.Contains(msg.Content, "[host:session-agent-enabled]") {
			n++
		}
	}
	return n
}

func TestAskContinuedTurnsAndBetweenTurnUpgradeDeliverAccurateContextAndOneTimeNotice(t *testing.T) {
	var h *Harness
	var id string
	var turnNum atomic.Int32
	var roundInTurn atomic.Int32
	mock := llm.NewRecordingClient(llm.NewKickDrivenMock(llm.KickDrivenConfig{
		Fallback: func(ctx context.Context, _ llm.Snapshot) modelcall.Completion {
			currentTurn := turnNum.Load()
			r := roundInTurn.Add(1)
			switch currentTurn {
			case 1:
				if r == 1 {
					return modelcall.Completion{ToolCalls: []api.ToolCall{
						{ID: "ask-read-1", Name: "command", Args: map[string]any{"command": "cat note.txt"}},
						{ID: "ask-refused-1", Name: "command", Args: map[string]any{"command": "sed -i 's/alpha/beta/' note.txt"}},
					}}
				}
				return modelcall.Completion{Content: "Turn 1 answer from Ask mode [note.txt:1]."}
			case 2:
				return modelcall.Completion{Content: "Turn 2 continued in Ask mode [note.txt:1]."}
			case 3:
				if r == 1 {
					return askRoundToolCall("agent-write-1", "command", map[string]any{"command": "printf 'upgraded\\n' > between.txt"})
				}
				if r == 2 {
					return askRoundToolCall("agent-progress-1", "update_progress", h.ProgressUpdateArgs(t, ctx, id, "## Progress\n- [x] between-turn upgrade\n"))
				}
				return modelcall.Completion{Content: MockCoordinatorCloseoutMarkdown("Completed between-turn upgrade write.", "")}
			default:
				return modelcall.Completion{Content: MockCoordinatorCloseoutMarkdown("Subsequent Agent turn completed.", "")}
			}
		},
	}))
	h = BuildForTest(t, WithLLMClient(mock), WithDecider(decide.Absent{}))
	ctx := h.OwnerCtx(t, t.Context())
	dir := h.ProjectDir(t, "ask-between-turn-upgrade")
	writeTestProjectApprovalsAllowWrite(t, dir)
	testutil.FailErr(t, "seed project source", os.WriteFile(filepath.Join(dir, "note.txt"), []byte("alpha\n"), 0o600))
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Mode: new(api.SessionModeAsk)}, dir)
	testutil.FailErr(t, "create Ask session", err)
	id = sess.ID
	h.SeedProgress(t, ctx, id)
	cancel := h.StartBackgroundWorkers(t, ctx)
	t.Cleanup(cancel)

	// 1. First Ask turn: standing context names Ask mode, no transition notice.
	turnNum.Store(1)
	roundInTurn.Store(0)
	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, id, "Inspect note.txt in Ask mode.")
	testutil.FailErr(t, "run first Ask turn", err)
	reqsAfterTurn1 := mock.AllRequests()
	if len(reqsAfterTurn1) < 2 {
		t.Fatalf("expected at least 2 requests in Turn 1, got %d", len(reqsAfterTurn1))
	}
	for i, req := range reqsAfterTurn1 {
		standing := requestStandingText(req)
		if !strings.Contains(standing, "## Ask mode") {
			t.Fatalf("Turn 1 request %d missing ## Ask mode:\n%s", i, standing)
		}
		if requestHasTransitionNotice(req) {
			t.Fatalf("Turn 1 request %d unexpectedly had transition notice", i)
		}
	}
	msgsTurn1, err := h.Store.GetMessages(ctx, id)
	testutil.FailErr(t, "get Turn 1 messages", err)
	var sawTurn1AskRefusal bool
	for _, msg := range msgsTurn1 {
		if msg.ToolResult != nil && msg.ToolResult.ToolCallID == "ask-refused-1" {
			sawTurn1AskRefusal = true
			body := msg.ToolResult.Content
			if body == "" {
				body = msg.Content
			}
			for _, want := range []string{
				"Rejected: `sed` (`-i`) at 1:1 is unavailable in Ask mode.",
				"Cause: Ask mode permits only read-only inspection and blocks `mutation`.",
				"Fix: Run `sed s/alpha/beta/ note.txt` to preview the transformed output on stdout without modifying the file in place.",
				"explain that the user can switch to Agent mode if they want you to apply the change.",
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("Turn 1 Ask refusal missing %q:\n%s", want, body)
				}
			}
		}
	}
	if !sawTurn1AskRefusal {
		t.Fatal("Turn 1 missing ask-refused-1 tool result")
	}

	// 2. Continued Ask turn: standing context still names Ask mode, no transition notice.
	turnNum.Store(2)
	roundInTurn.Store(0)
	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, id, "Continue in Ask mode.")
	testutil.FailErr(t, "run second Ask turn", err)
	reqsAfterTurn2 := mock.AllRequests()
	if len(reqsAfterTurn2) <= len(reqsAfterTurn1) {
		t.Fatalf("expected new request in Turn 2")
	}
	turn2Req := reqsAfterTurn2[len(reqsAfterTurn1)]
	if !strings.Contains(requestStandingText(turn2Req), "## Ask mode") || requestHasTransitionNotice(turn2Req) {
		t.Fatalf("Turn 2 continued Ask request had wrong orientation or transition notice")
	}

	// 3. Between-turn ask -> agent upgrade: Turn 3's first request uses Agent standing context,
	// full command schema, and carries session.mode.agent_enabled once.
	_, err = h.Store.SetMode(ctx, id, api.SessionModeAgent)
	testutil.FailErr(t, "switch Ask -> Agent between turns", err)
	turnNum.Store(3)
	roundInTurn.Store(0)
	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, id, "Now write between.txt in Agent mode.")
	testutil.FailErr(t, "run third turn in Agent mode", err)
	reqsAfterTurn3 := mock.AllRequests()
	if len(reqsAfterTurn3) <= len(reqsAfterTurn2) {
		t.Fatalf("expected new requests in Turn 3")
	}
	turn3FirstReq := reqsAfterTurn3[len(reqsAfterTurn2)]
	if strings.Contains(requestStandingText(turn3FirstReq), "## Ask mode") {
		t.Fatalf("Turn 3 Agent request still carried ## Ask mode")
	}
	if countRequestTransitionNotices(turn3FirstReq) != 1 {
		t.Fatalf("Turn 3 first Agent request transition notices = %d, want 1", countRequestTransitionNotices(turn3FirstReq))
	}

	// 4. Refused agent -> ask downgrade produces no transition notice, and Turn 4 in Agent mode has no duplicate notice.
	if _, err := h.Store.SetMode(ctx, id, api.SessionModeAsk); !errors.Is(err, store.ErrSessionModeLocked) {
		t.Fatalf("expected ErrSessionModeLocked on Agent -> Ask downgrade, got %v", err)
	}
	turnNum.Store(4)
	roundInTurn.Store(0)
	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, id, "Confirm Agent mode stays active without duplicate notice.")
	testutil.FailErr(t, "run fourth turn in Agent mode", err)
	msgs, err := h.Store.GetMessages(ctx, id)
	testutil.FailErr(t, "get transcript messages", err)
	totalNudges := 0
	for _, msg := range msgs {
		if msg.HostSignalID == "session.mode.agent_enabled" {
			totalNudges++
		}
	}
	if totalNudges != 1 {
		t.Fatalf("total session.mode.agent_enabled transcript messages across turns = %d, want 1", totalNudges)
	}

	// 5. An ordinary Agent session (created in Ask mode, switched to Agent before any turn starts)
	// never emits session.mode.agent_enabled.
	ordinarySess, err := h.Store.Create(ctx, api.CreateSessionRequest{Mode: new(api.SessionModeAsk)}, sess.ProjectID)
	testutil.FailErr(t, "create unstarted Ask session", err)
	_, err = h.Store.SetMode(ctx, ordinarySess.ID, api.SessionModeAgent)
	testutil.FailErr(t, "switch unstarted Ask session to Agent", err)
	h.SeedProgress(t, ctx, ordinarySess.ID)
	turnNum.Store(5)
	roundInTurn.Store(0)
	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, ordinarySess.ID, "Run ordinary Agent turn.")
	testutil.FailErr(t, "run ordinary Agent turn", err)
	ordinaryMsgs, err := h.Store.GetMessages(ctx, ordinarySess.ID)
	testutil.FailErr(t, "get ordinary Agent transcript messages", err)
	for _, msg := range ordinaryMsgs {
		if msg.HostSignalID == "session.mode.agent_enabled" || strings.Contains(msg.Content, "[host:session-agent-enabled]") {
			t.Fatalf("ordinary Agent session unexpectedly received session.mode.agent_enabled notice: %+v", msg)
		}
	}
}

func verifyAskUpgradeTranscript(t *testing.T, messages []api.Message) {
	t.Helper()
	nudges := 0
	var readOK, writeRefused, externalRefused bool
	for _, msg := range messages {
		if msg.HostSignalID == "session.mode.agent_enabled" {
			nudges++
		}
		if res := msg.ToolResult; res != nil {
			body := res.Content
			if body == "" {
				body = msg.Content
			}
			switch res.ToolCallID {
			case "read":
				readOK = strings.Contains(body, "source evidence")
			case "blocked-write":
				writeRefused = slices.Contains(res.Codes, "COMMAND_EFFECT_NOT_ALLOWED") || strings.Contains(body, "COMMAND_EFFECT_NOT_ALLOWED")
				if !strings.Contains(body, "was refused under launch-time Ask authority") || !strings.Contains(body, "kept its launch-time read-only authority across the session upgrade") {
					t.Fatalf("already-upgraded round-1 redirect refusal missing past-tense Ask attribution:\n%s", body)
				}
				if strings.Contains(body, "switch to Agent mode") {
					t.Fatalf("already-upgraded round-1 redirect refusal still directed switching to Agent mode:\n%s", body)
				}
			case "blocked-external":
				externalRefused = slices.Contains(res.Codes, tools.AskModeActiveCode) || strings.Contains(body, tools.AskModeActiveCode)
				if strings.Contains(body, "switch to Agent mode") {
					t.Fatalf("already-upgraded round-1 external refusal still directed switching to Agent mode:\n%s", body)
				}
				if !strings.Contains(body, "refused under launch-time Ask authority") {
					t.Fatalf("already-upgraded round-1 external refusal missing launch-time Ask authority explanation:\n%s", body)
				}
			}
		}
	}
	if nudges != 1 {
		t.Fatalf("Agent upgrade nudges = %d", nudges)
	}
	if !readOK || !writeRefused || !externalRefused {
		t.Fatalf("round 1 in-flight Ask enforcement: readOK=%t writeRefused=%t externalRefused=%t", readOK, writeRefused, externalRefused)
	}
}
