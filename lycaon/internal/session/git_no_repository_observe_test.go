package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/tools"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestACOP01WriteLegGitWarningAnnotatesTheCommand(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	sess := &api.Session{ID: "sess-write-leg-git"}
	const output = `{"status":128}`
	for _, offered := range []bool{true, false} {
		names := []string{"command"}
		if offered {
			names = append(names, "source_history")
		}
		ctx := tools.WithCallReach(context.Background(), toolreach.Offered(names))
		raised := guidance.ToolResultFacts{}.WithFeedback(toolrejection.GitNoRepositoryOnWriteLegCode, nil, nil)
		out, facts := mgr.ToolPolicy.AfterTool(ctx, sess, "command", map[string]any{"command": "git status"}, output, 1, raised)
		if !facts.HasCode(toolrejection.GitNoRepositoryOnWriteLegCode) || !facts.Succeeded() {
			t.Fatalf("facts = %+v, want the warning on a result that still succeeded", facts)
		}
		if strings.Contains(out, "`source_history(") != offered {
			t.Fatalf("offered=%v, warning:\n%s", offered, out)
		}
	}
	if plain, _ := mgr.ToolPolicy.AfterTool(context.Background(), sess, "command", map[string]any{"command": "git status"}, output, 1, guidance.ToolResultFacts{}); plain != output {
		t.Fatalf("a call with no miss was annotated: %q", plain)
	}
}

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
