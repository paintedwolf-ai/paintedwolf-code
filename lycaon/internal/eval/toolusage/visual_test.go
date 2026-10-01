package toolusage

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/logview"
)

func TestVisualCallsDistinguishCaptureFromOrdinaryCommands(t *testing.T) {
	events := []logview.ToolEvent{
		{Name: "command", Args: json.RawMessage(`{"command":"app"}`)},
		{Name: "command", Args: json.RawMessage(`{"terminal_capture":null}`)},
		{Name: "command", Args: json.RawMessage(`{"terminal_capture":false}`)},
		{Name: "command", Args: json.RawMessage(`{"terminal_capture":{}}`)},
		{Name: "command", Args: json.RawMessage(`{"terminal_capture":{},"capability_request":{"direct_ip":{"declared_destinations":["udp://example.test:123"]}}}`)},
		{Name: "command", Args: json.RawMessage(`{"capability_request":{"write_root":{}}}`)},
		{Name: "terminal_open"},
		{Name: "terminal_open", Args: json.RawMessage(`{"command":"app"}`)},
		{Name: "terminal_open", Args: json.RawMessage(`{"command":"app","observe":"screen"}`)},
		{Name: "terminal_open", Args: json.RawMessage(`{"command":"app","observe":"ack"}`)},
		{Name: "terminal_send", Args: json.RawMessage(`{"id":"pty","text":"x"}`)},
		{Name: "terminal_send", Args: json.RawMessage(`{"id":"pty","text":"x","observe":"delta"}`)},
		{Name: "terminal_snapshot"},
		{Name: "capture_page"},
		{Name: "page_snapshot"},
		{Name: "render_view"},
	}
	var got VisualMetrics
	analyzeVisualCalls(&got, events)
	want := VisualMetrics{SealedTerminalCalls: 2, SealedWithCapabilityCalls: 1, HeldSnapshotCalls: 1, HeldScreenCalls: 3, PageCaptureCalls: 2}
	if got != want {
		t.Fatalf("visual attempts = %+v, want %+v", got, want)
	}
}

func TestAggregateVisualAttempts(t *testing.T) {
	profile := AggregateProfiles([]Profile{
		{Visual: VisualMetrics{SealedTerminalCalls: 2, HeldSnapshotCalls: 1, HeldScreenCalls: 2}},
		{Visual: VisualMetrics{PageCaptureCalls: 4}},
	})
	if profile.Aggregates.SealedTerminalCalls.Mean != 1 ||
		profile.Aggregates.HeldSnapshotCalls.Mean != 0.5 ||
		profile.Aggregates.HeldScreenCalls.Mean != 1 ||
		profile.Aggregates.PageCaptureCalls.Mean != 2 {
		t.Fatalf("visual aggregates = %+v", profile.Aggregates)
	}
}
