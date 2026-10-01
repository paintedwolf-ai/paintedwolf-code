package toolusage

import (
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/logview"
)

func analyzeVisualCalls(metrics *VisualMetrics, events []logview.ToolEvent) {
	for _, event := range events {
		switch strings.TrimSpace(event.Name) {
		case "command":
			var args struct {
				Capture    map[string]json.RawMessage `json:"terminal_capture"`
				Capability map[string]json.RawMessage `json:"capability_request"`
			}
			if json.Unmarshal(event.Args, &args) != nil || args.Capture == nil {
				continue
			}
			metrics.SealedTerminalCalls++
			if len(args.Capability) > 0 {
				metrics.SealedWithCapabilityCalls++
			}
		case "terminal_snapshot":
			metrics.HeldSnapshotCalls++
		case "terminal_open", "terminal_send":
			var args struct {
				Observe *string `json:"observe"`
			}
			if json.Unmarshal(event.Args, &args) == nil && (args.Observe == nil || *args.Observe == "screen") {
				metrics.HeldScreenCalls++
			}
		case "capture_page", "page_snapshot":
			metrics.PageCaptureCalls++
		}
	}
}
