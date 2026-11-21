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
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/llmtest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAskRoundUpgradeEnablesNativeWriteAndRewind(t *testing.T) {
	var h *Harness
	var id string
	var round atomic.Int32
	mock := llmtest.NewRecordingClient(llmtest.NewKickDrivenMock(llmtest.KickDrivenConfig{
		Fallback: func(ctx context.Context, _ llmtest.Snapshot) modelcall.Completion {
			switch round.Add(1) {
			case 1:
				// Commit the upgrade in the store while round 1's Ask response is in flight.
				// Round 1 must still execute its tool calls under its frozen Ask authority.
				if _, err := h.Store.Sessions.SetMode(ctx, id, api.SessionModeAgent); err != nil {
					t.Errorf("commit live mode upgrade: %v", err)
				}
				return modelcall.Completion{ToolCalls: []api.ToolCall{
					{ID: "read", Name: "command", Args: map[string]any{"command": "cat note.txt"}},
					{ID: "blocked-write", Name: "command", Args: map[string]any{"command": "printf 'escaped\\n' > escaped.txt"}},
					{ID: "blocked-external", Name: "command", Args: map[string]any{"command": "/bin/echo outside"}},
				}}
			case 2:
				return askRoundToolCall("plan", "update_progress", h.ProgressUpdateArgs(t, ctx, id, "## Progress\n- [ ] wiring test plan\n"))
			case 3:
				return askRoundToolCall("write", "command", map[string]any{"command": "printf 'created\n' > result.txt"})
			case 4:
				return askRoundToolCall("progress", "update_progress", h.ProgressUpdateArgs(t, ctx, id, "## Progress\n- [x] wiring test plan\n"))
			default:
				return modelcall.Completion{Content: MockCoordinatorCloseoutMarkdown("Wrote the requested result.", "")}
			}
		},
	}))
	h = BuildForTest(t, WithLLMClient(mock), WithDecider(decide.Absent{}))
	ctx := h.OwnerCtx(t, t.Context())
	dir := h.ProjectDir(t, "ask-upgrade")
	WriteTestProjectApprovalsAllowWrite(t, dir)
	testutil.FailErr(t, "seed project source", os.WriteFile(filepath.Join(dir, "note.txt"), []byte("source evidence\n"), 0o600))
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Mode: new(api.SessionModeAsk)}, dir)
	testutil.FailErr(t, "create Ask session", err)
	id = sess.ID
	h.Start(t)

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
	messages, err := h.Store.Transcript.GetMessages(ctx, id)
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
	if !slices.Contains(round2Cmd.DescriptionParts, "mutation") {
		t.Fatalf("round 2 Agent command DescriptionParts = %v, expected mutation", round2Cmd.DescriptionParts)
	}
	round2Props, _ := round2Cmd.ArgsSchema["properties"].(map[string]any)
	for _, restored := range []string{"capability_request", "verification"} {
		if _, present := round2Props[restored]; !present {
			t.Fatalf("round 2 Agent command schema missing restored field %q", restored)
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
			case "blocked-external":
				externalRefused = slices.Contains(res.Codes, tools.AskModeActiveCode) || strings.Contains(body, tools.AskModeActiveCode)
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
