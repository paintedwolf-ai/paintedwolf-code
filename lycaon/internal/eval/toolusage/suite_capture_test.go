package toolusage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/logview"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCoordinatorCaptureAttribution(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"text answer", `{"completion":{"content_chars":20}}`, true},
		{"discarded tool call", `{"completion":{"content_chars":116,"tool_calls":[{"name":"write"}]},"tool_names":["read"]}`, true},
		{"tool only", `{"completion":{"tool_calls":[{"name":"read"}]}}`, true},
		{"earlier call in same session", `{"ts":"2020-01-01T00:00:00Z"}`, true},
		{"nonstreamed call", `{"call":"complete"}`, true},
		{"auxiliary title", `{"call":"session_title"}`, false},
		{"no surface", `{"surface":""}`, false},
		{"other session", `{"session_id":"other"}`, false},
		{"worker", `{"agent_type":"worker"}`, false},
		{"unknown role", `{"agent_type":""}`, false},
		{"no model", `{"model":""}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := logview.LLMRecord{SessionID: "s", AgentType: "coordinator", Surface: "implement_investigate", Call: "stream", Model: "actual"}
			testutil.FailErr(t, "decode capture", json.Unmarshal([]byte(tc.body), &row))
			if hasCoordinatorCapture([]logview.LLMRecord{row}, "s") != tc.want {
				t.Fatal("incorrect coordinator attribution")
			}
			if hasCoordinatorCapture([]logview.LLMRecord{row}, "") {
				t.Fatal("attributed an empty session")
			}
		})
	}
}

func TestAwaitCoordinatorCaptureRequiresAttribution(t *testing.T) {
	directory := t.TempDir()
	writeSuiteCapture(t, directory, "actual")
	testutil.FailErr(t, "accept mixed text and tool capture", awaitSuiteCoordinatorCapture(t.Context(), directory, "s"))
	testutil.FailErr(t, "replace capture with auxiliary call", os.WriteFile(filepath.Join(directory, "llm-requests.jsonl"),
		[]byte("{\"session_id\":\"s\",\"agent_type\":\"coordinator\",\"call\":\"session_title\",\"model\":\"utility\"}\n"), 0o600))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := awaitSuiteCoordinatorCapture(ctx, directory, "s"); !errors.Is(err, context.Canceled) {
		t.Fatalf("missing coordinator attribution: %v", err)
	}
}
