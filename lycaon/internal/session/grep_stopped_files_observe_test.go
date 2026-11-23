package session

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestGrepStoppedFilesWarnsOnACompletedCommand(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	sess := &api.Session{ID: "sess-grep-stopped"}
	const output = `{"status":2}`
	raised := guidance.ToolResultFacts{}.WithFeedback(toolrejection.GrepStoppedFilesCode, map[string]any{
		"grep_input_path": "b.txt", "grep_stop_line": 2, "grep_record_bytes": int64(200001), "grep_stop_resource": "regex_work_steps",
		"grep_stop_memory": false, "grep_lines_emitted_before_stop": 1, "grep_pattern_literal": false, "grep_stopped_count": 1,
		"grep_stopped_inputs": []string{"b.txt:2"}, "grep_searched_files": 3, "grep_matched_files": 3,
	}, nil)
	out, facts := mgr.ToolPolicy.AfterTool(context.Background(), sess, "command", map[string]any{"command": "grep -r x ."}, output, 1, raised)
	if !facts.HasCode(toolrejection.GrepStoppedFilesCode) || out == output {
		t.Fatalf("facts = %+v, out = %q", facts, out)
	}
}
