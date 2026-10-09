package session

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const messageStorageSecret = "ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3"

func TestUpdateMessageReappliesStorageRedaction(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	mgr := NewManager(mem, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.Transcript.SetRedactor(redactMessageStorageFixture)
	sess, err := mem.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	placeholder := api.Message{ID: "assistant-1", Role: api.MessageRoleAssistant}
	testutil.FailErr(t, "append placeholder", mgr.Transcript.Append(ctx, sess.ID, placeholder))
	raw := placeholder
	raw.ToolCalls = []api.ToolCall{{
		ID: "call-write", Name: "write",
		Args: map[string]any{"content": "TOKEN=" + messageStorageSecret},
	}}
	testutil.FailErr(t, "update assistant tool call", mgr.Transcript.Update(ctx, sess.ID, raw.ID, raw))

	messages, err := mem.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read messages", err)
	if len(messages) != 1 || len(messages[0].ToolCalls) != 1 {
		t.Fatalf("stored messages = %+v", messages)
	}
	content, _ := messages[0].ToolCalls[0].Args["content"].(string)
	if strings.Contains(content, messageStorageSecret) || !strings.Contains(content, "[REDACTED]") {
		t.Fatalf("stored tool-call content = %q", content)
	}
	if messages[0].HostSecretRedaction == nil || messages[0].HostSecretRedaction.Occurrences() != 1 {
		t.Fatalf("redaction provenance = %+v", messages[0].HostSecretRedaction)
	}
}

func redactMessageStorageFixture(_ context.Context, msg api.Message) (api.Message, bool) {
	out := msg
	changed := false
	if len(msg.ToolCalls) > 0 {
		out.ToolCalls = append([]api.ToolCall(nil), msg.ToolCalls...)
		for i := range out.ToolCalls {
			out.ToolCalls[i].Args = make(map[string]any, len(msg.ToolCalls[i].Args))
			for key, value := range msg.ToolCalls[i].Args {
				if text, ok := value.(string); ok {
					redacted := strings.ReplaceAll(text, messageStorageSecret, "[REDACTED]")
					changed = changed || redacted != text
					value = redacted
				}
				out.ToolCalls[i].Args[key] = value
			}
		}
	}
	if changed {
		out.HostSecretRedaction = api.NewHostSecretRedactionMeta([]api.RedactedSpan{{Field: "content", Start: 0, Length: 10, Kind: api.RedactionKindSecret}})
	}
	return out, changed
}
