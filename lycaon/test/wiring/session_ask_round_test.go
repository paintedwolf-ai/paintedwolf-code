package wiring

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
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
				// Commit the upgrade after the model has its Ask request.
				if _, err := h.Store.SetMode(ctx, id, api.SessionModeAgent); err != nil {
					t.Errorf("commit live mode upgrade: %v", err)
				}
				return askRoundToolCall("read", "inspect_source", map[string]any{"operation": "read", "path": "note.txt"})
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
	has := func(request modelcall.CompletionRequest, name string) bool {
		return slices.ContainsFunc(request.Tools, func(tool tools.ToolMeta) bool { return tool.Name == name })
	}
	if has(requests[0], "command") || !has(requests[0], "inspect_source") || !has(requests[1], "command") {
		t.Fatalf("tool offers through upgrade: round 1 %+v, round 2 %+v", requests[0].Tools, requests[1].Tools)
	}
}

func verifyAskUpgradeTranscript(t *testing.T, messages []api.Message) {
	t.Helper()
	nudges := 0
	for _, msg := range messages {
		if msg.HostSignalID == "session.mode.agent_enabled" {
			nudges++
		}
	}
	if nudges != 1 {
		t.Fatalf("Agent upgrade nudges = %d", nudges)
	}
}
