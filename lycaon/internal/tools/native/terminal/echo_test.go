package terminal

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

func TestSendResultReportsNoPayloadLength(t *testing.T) {
	encoded, err := surveyjson.Marshal(SendResult{ID: "term-1", Running: true, Observe: terminalObserveAck})
	if err != nil {
		t.Fatalf("marshal send result: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode send result: %v", err)
	}
	for key, value := range decoded {
		if _, ok := value.(float64); ok {
			t.Fatalf("terminal_send reports a number derived from the written payload: %q", key)
		}
	}
	if _, ok := decoded["bytes"]; ok {
		t.Fatal("terminal_send still reports a payload byte count")
	}
}

func TestOpenEchoesTheCanonicalCommand(t *testing.T) {
	canonical := "curl -H Authorization:{{paintedwolf-secret:0f7a2f1c-1a11-4a1a-9a1a-2b3c4d5e6f70}} https://example.test"
	echo := canonicalCommandEcho(tools.ToolContext{
		CanonicalArgs: map[string]any{"command": "  " + canonical + "  "},
	})
	if echo != canonical {
		t.Fatalf("echo = %q, want the canonical command line", echo)
	}
	if !strings.Contains(echo, "{{paintedwolf-secret:") {
		t.Fatal("the reference was not preserved in the echo")
	}
}

func TestOpenOmitsTheCommandWithoutACanonicalStamp(t *testing.T) {
	if echo := canonicalCommandEcho(tools.ToolContext{}); echo != "" {
		t.Fatalf("echo without a canonical stamp = %q", echo)
	}
	encoded, err := surveyjson.Marshal(OpenResult{ID: "term-1", Running: true})
	if err != nil {
		t.Fatalf("marshal open result: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode open result: %v", err)
	}
	if _, ok := decoded["command"]; ok {
		t.Fatal("terminal_open reported a command with nothing canonical to report")
	}
}
