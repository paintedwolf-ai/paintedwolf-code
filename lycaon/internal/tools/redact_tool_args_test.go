package tools_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRedactPersistedToolCallArgsTerminalSend(t *testing.T) {
	in := []api.ToolCall{{
		Name: "terminal_send",
		ID:   "1",
		Args: map[string]any{"id": "abc", "input": "s3cret-token"},
	}, {
		Name: "terminal_read",
		ID:   "2",
		Args: map[string]any{"id": "abc"},
	}}
	out, spans := messageview.RedactToolCallArgs(in)
	if len(spans) != 1 || spans[0].Kind != api.RedactionKindObserverMask {
		t.Fatalf("mask recorded no observer span: %+v", spans)
	}
	if out[0].Args["input"] != messageview.RedactedToolArgPlaceholder {
		t.Fatalf("terminal_send input = %v, want redacted", out[0].Args["input"])
	}
	if in[0].Args["input"] != "s3cret-token" {
		t.Fatal("redaction mutated the execution args slice")
	}
	if out[1].Args["id"] != "abc" {
		t.Fatalf("terminal_read args changed: %v", out[1].Args)
	}
}

func TestRedactMessageForObserverTerminalSend(t *testing.T) {
	msg := api.Message{
		Role: api.MessageRoleAssistant,
		ToolCalls: []api.ToolCall{{
			Name: "terminal_send",
			Args: map[string]any{"id": "pty", "input": "Alice{Enter}"},
		}},
	}
	out := messageview.RedactMessage(msg)
	if out.ToolCalls[0].Args["input"] != messageview.RedactedToolArgPlaceholder {
		t.Fatalf("observer input = %v", out.ToolCalls[0].Args["input"])
	}
	if msg.ToolCalls[0].Args["input"] != "Alice{Enter}" {
		t.Fatal("observer redaction mutated source message")
	}
}
