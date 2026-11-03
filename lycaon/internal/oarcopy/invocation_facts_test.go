package oarcopy

import (
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestForeignCapabilitySnapshotsCannotDescribeTheReceivingTool(t *testing.T) {
	for _, tool := range []string{"", "read", "command"} {
		facts := FactsFromData(map[string]any{
			"tool": "command", "tool_args": map[string]any{},
			CapabilitySnapshotKey: toolcontract.ToolCapabilitySnapshot{
				Tool: tool, Capabilities: toolcontract.CapabilityLoopbackConnect,
			},
		})
		known := tool == "command"
		if facts["paintedwolf.capability_contract_known"] != known || facts["paintedwolf.supports_loopback_connect"] != known {
			t.Fatalf("foreign contract changed receiver capabilities: %#v", facts)
		}
		fields := facts["paintedwolf.capability_request_fields"].([]string)
		if (len(fields) != 0) != known {
			t.Fatalf("foreign contract leaked request fields: %v", fields)
		}
		if _, leaked := facts["paintedwolf."+CapabilitySnapshotKey]; leaked {
			t.Fatal("local presentation carrier became an OAR fact")
		}
	}
}

func TestPublishInvocationRecoveryMarksOnlyEditSourceRepair(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		schemaTool, tool string
		want             bool
	}{
		{"edit", "edit", true},
		{"edit", "write", false},
		{"write", "edit", false},
		{"", "edit", false},
	} {
		facts := map[string]any{"paintedwolf.argument_schema_tool": tc.schemaTool}
		PublishInvocationRecovery(facts, tc.tool, nil, nil)
		if got := facts["paintedwolf.argument_schema_edits_source"]; got != tc.want {
			t.Fatalf("schema %q tool %q: got %v", tc.schemaTool, tc.tool, got)
		}
	}
}
