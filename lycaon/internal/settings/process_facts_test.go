package settings

import (
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
)

func TestProcessSpawningFactComesFromManifest(t *testing.T) {
	for _, tool := range []string{"command", "verify", "terminal_open", "scan_pack", "render_view", "capture_page", "measure_page", "page_open"} {
		if !containmentFor(hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: tool,
},
}).SpawnsProcess {
			t.Fatalf("%s missing process-spawning fact", tool)
		}
	}
	for _, tool := range []string{"read", "write", "grep", "page_snapshot"} {
		if containmentFor(hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: tool,
},
}).SpawnsProcess {
			t.Fatalf("%s incorrectly marked process-spawning", tool)
		}
	}
}
