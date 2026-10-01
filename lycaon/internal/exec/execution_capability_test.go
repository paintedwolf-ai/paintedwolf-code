package exec

import (
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
)

func TestHostExecutionBelongsOnlyToAgentCommandLaunch(t *testing.T) {
	boundary := &confine.Confinement{HostExecution: true, Network: confine.NetworkDirectIP}
	for _, kind := range []LaunchKind{LaunchAgentCommand, LaunchHostInternal, LaunchBundledScanner, LaunchExternalScanner, LaunchLocalMCP, LaunchManagedBrowser} {
		plan := AgentLaunch(kind, "reviewed execution", boundary)
		err := plan.validate()
		if (err == nil) != (kind == LaunchAgentCommand) {
			t.Fatalf("host execution on %s: %v", kind, err)
		}
	}
}
