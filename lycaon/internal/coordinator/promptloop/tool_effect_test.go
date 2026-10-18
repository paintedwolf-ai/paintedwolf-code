package promptloop

import (
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCommittedEffectUsesProgressStatus(t *testing.T) {
	for _, tc := range []struct {
		name, tool, output string
		want               bool
	}{
		{"updated", "update_progress", `{"status":"updated"}`, true},
		{"unchanged", "update_progress", `{"status":"unchanged"}`, false},
		{"unrelated text", "update_progress", `{"status":"updated","note":"unchanged"}`, true},
		{"unknown", "update_progress", `{"status":"unknown"}`, false},
		{"missing", "update_progress", `{}`, false},
		{"invalid", "update_progress", `updated`, false},
		{"write", "write", `ok`, true},
		{"read", "read", `ok`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contract, ok := toolcontract.Lookup(tc.tool)
			if !ok {
				t.Fatalf("missing contract for %s", tc.tool)
			}
			run := completedToolRun{call: api.ToolCall{Name: tc.tool}, contract: contract, output: tc.output}
			if got := run.committedEffect(); got != tc.want {
				t.Fatalf("committedEffect = %v, want %v", got, tc.want)
			}
		})
	}
}
