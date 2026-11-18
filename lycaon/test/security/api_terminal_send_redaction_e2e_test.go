package security

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/exposure"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

// Model history keeps terminal input; every observer path (transcript page,
// published message event, LLM capture) carries the mask instead.
func TestTerminalSendInputRedactedOnObserverPaths(t *testing.T) {
	const secret = "pty-secret-passphrase-do-not-persist"
	capturePath := filepath.Join(t.TempDir(), "llm-requests.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", capturePath)
	observability.CloseLLMDebug()
	t.Cleanup(observability.CloseLLMDebug)
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: "drive the menu",
		ToolCalls: []llm.MockToolCall{{
			ID:   "ts1",
			Name: "terminal_send",
			Args: map[string]any{"id": "missing-handle", "input": secret},
		}},
		FollowUpText: "drove",
	}}})
	h := wiring.BuildForTest(t, wiring.WithLLMClient(mock))
	ctx := context.Background()
	dir := t.TempDir()

	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	if err := h.Sessions.Manager.Chats.SetAgentType(ctx, sess.ID, "implementer"); err != nil {
		testutil.FailErr(t, "SetAgentType", err)
	}
	activation := h.Sessions.Manager.Coordinator.Loading.Ledger
	activation.Activate(sess.ID, []string{"terminal_send"}, "type into the terminal")

	ch, unsub, err := h.MemoryHub().Subscribe(ctx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "Subscribe", err)
	defer unsub()

	if _, err := h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "drive the menu"); err != nil {
		testutil.FailErr(t, "Prompt", err)
	}

	// Published message events carry the transcript projection.
	foundEvent := false
	testutil.WaitFor(t, "terminal_send message event", func() bool {
		select {
		case envelope, ok := <-ch:
			if !ok {
				return false
			}
			exposure.Absent(t, string(envelope.Data), secret)
			if envelope.Topic != wire.EventTopicMessage {
				return false
			}
			var ev wire.MessageEvent
			testutil.FailErr(t, "unmarshal message event", json.Unmarshal(envelope.Data, &ev))
			for _, call := range ev.Message.ToolCalls {
				if call.Name == "terminal_send" {
					if call.Args["input"] != messageview.RedactedToolArgPlaceholder {
						t.Fatalf("event terminal_send.input = %v want the mask", call.Args["input"])
					}
					foundEvent = true
				}
			}
			return foundEvent
		default:
			return false
		}
	})

	// The LLM capture is written once the follow-up request carrying the call
	// has been sent; Prompt returned after that request.
	observability.CloseLLMDebug()
	capture, err := os.ReadFile(capturePath)
	testutil.FailErr(t, "read LLM capture", err)
	exposure.Absent(t, string(capture), secret)
	foundCapture := false
	lines := bufio.NewScanner(bytes.NewReader(capture))
	lines.Buffer(nil, 64<<20)
	for lines.Scan() {
		var entry struct {
			Messages []wire.Message `json:"messages"`
		}
		testutil.FailErr(t, "unmarshal LLM capture entry", json.Unmarshal(lines.Bytes(), &entry))
		for _, m := range entry.Messages {
			for _, call := range m.ToolCalls {
				if call.Name == "terminal_send" {
					if call.Args["input"] != messageview.RedactedToolArgPlaceholder {
						t.Fatalf("capture terminal_send.input = %v want the mask", call.Args["input"])
					}
					foundCapture = true
				}
			}
		}
	}
	testutil.FailErr(t, "scan LLM capture", lines.Err())
	if !foundCapture {
		t.Fatal("LLM capture holds no terminal_send call")
	}

	// Durable model history retains the input.
	storeMsgs, err := h.Store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "Store.GetMessages", err)
	foundStore := false
	for _, m := range storeMsgs {
		if m.Role != wire.MessageRoleAssistant {
			continue
		}
		for _, call := range m.ToolCalls {
			if call.Name != "terminal_send" {
				continue
			}
			foundStore = true
			raw, _ := call.Args["input"].(string)
			if raw != secret {
				t.Fatalf("store terminal_send.input = %q want cleartext for model history", raw)
			}
		}
	}
	if !foundStore {
		t.Fatalf("expected store assistant terminal_send tool call; msgs=%+v", storeMsgs)
	}

	// Transcript projection redacts by tool identity.
	page, err := h.Sessions.Manager.Runner.Transcript.GetTranscriptPage(ctx, sess.ID, wire.TranscriptPageQuery{})
	testutil.FailErr(t, "GetTranscriptPage", err)
	foundTranscript := false
	for _, m := range page.Messages {
		if m.Role != wire.MessageRoleAssistant {
			continue
		}
		for _, call := range m.ToolCalls {
			if call.Name != "terminal_send" {
				continue
			}
			foundTranscript = true
			raw, _ := call.Args["input"].(string)
			if raw == secret || strings.Contains(raw, secret) {
				t.Fatalf("transcript terminal_send.input still cleartext: %q", raw)
			}
			if raw != messageview.RedactedToolArgPlaceholder {
				t.Fatalf("transcript terminal_send.input = %q want %q", raw, messageview.RedactedToolArgPlaceholder)
			}
		}
	}
	if !foundTranscript {
		t.Fatalf("expected transcript terminal_send tool call; msgs=%+v", page.Messages)
	}
}
