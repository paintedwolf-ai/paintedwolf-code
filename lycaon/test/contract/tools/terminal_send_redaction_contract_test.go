package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestTerminalSendToolCallArgsAlwaysRedacted checks tool-identity redaction.
func TestTerminalSendToolCallArgsAlwaysRedacted(t *testing.T) {
	t.Parallel()
	secret := "super-secret-token-value"
	calls := []api.ToolCall{{
		Name: "terminal_send",
		ID:   "tc1",
		Args: map[string]any{"id": "pty-1", "input": secret},
	}, {
		Name: "command",
		ID:   "tc2",
		Args: map[string]any{"command": "echo hi"},
	}}
	out, spans := messageview.RedactToolCallArgs(calls)
	if len(spans) != 1 || spans[0].Kind != api.RedactionKindObserverMask {
		t.Fatalf("mask recorded no observer span: %+v", spans)
	}
	if out[0].Args["input"] != messageview.RedactedToolArgPlaceholder {
		t.Fatalf("terminal_send input = %v want %q", out[0].Args["input"], messageview.RedactedToolArgPlaceholder)
	}
	if calls[0].Args["input"] != secret {
		t.Fatal("redaction must not mutate execution args")
	}
	if out[1].Args["command"] != "echo hi" {
		t.Fatalf("command args mutated: %v", out[1].Args)
	}
}

func TestTerminalSendRedactionWiredOnObserverPaths(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	paths := []string{
		filepath.Join(root, "lycaon", "internal", "session", "manager_crud.go"),
		filepath.Join(root, "lycaon", "internal", "events", "publish_message.go"),
		filepath.Join(root, "lycaon", "internal", "observability", "llm_capture.go"),
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		contractcheck.FailErr(t, "read "+path, err)
		text := string(data)
		// Capture redaction also scrubs message content.
		if !strings.Contains(text, "messageview.RedactMessage") &&
			!strings.Contains(text, "messageview.RedactMessages") &&
			!strings.Contains(text, "messageview.TranscriptMessage") &&
			!strings.Contains(text, "RedactMessagesForCapture") {
			t.Fatalf("%s is missing observer redaction", path)
		}
	}
	captureSrc := filepath.Join(root, "lycaon", "internal", "observability", "capture_redact.go")
	captureData, err := os.ReadFile(captureSrc)
	contractcheck.FailErr(t, "read capture_redact.go", err)
	if !strings.Contains(string(captureData), "messageview.RedactMessages") {
		t.Fatal("capture_redact.go is missing message projection")
	}
	redactSrc := filepath.Join(root, "lycaon", "internal", "messageview", "redact.go")
	data, err := os.ReadFile(redactSrc)
	contractcheck.FailErr(t, "read redact_tool_args.go", err)
	text := string(data)
	for _, marker := range []string{`"terminal_send"`, "RedactedToolArgPlaceholder", `"input"`, "RedactMessage"} {
		if !strings.Contains(text, marker) {
			t.Fatalf("%s missing %q", redactSrc, marker)
		}
	}
}
