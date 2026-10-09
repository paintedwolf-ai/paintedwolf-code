package session

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEditorConfigMismatchCardAnnotatesTheLandedWrite(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	sess := &api.Session{ID: "sess-editorconfig"}
	raised := guidance.ToolResultFacts{}.WithFeedback(tools.EditorConfigMismatchCode, map[string]any{
		"path":               "tests/test_image.py",
		"editorconfig_rules": []string{"trim_trailing_whitespace", "indent_style"},
		"editorconfig_lines": "tests/test_image.py:16,28 trim_trailing_whitespace; tests/test_image.py:16,28 indent_style",
	}, &api.FeedbackSubject{Kind: "file", ID: "tests/test_image.py"})

	out, facts := mgr.ToolPolicy.AfterTool(context.Background(), sess, "write",
		map[string]any{"path": "tests/test_image.py"}, "Wrote 1200 bytes to tests/test_image.py", 1, raised)
	for _, want := range []string{
		"Wrote 1200 bytes to tests/test_image.py",
		"Code: EDITORCONFIG_MISMATCH",
		"(trim_trailing_whitespace, indent_style)",
		"tests/test_image.py:16,28 indent_style",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if !facts.HasCode(tools.EditorConfigMismatchCode) {
		t.Fatalf("facts = %#v, want the mismatch code", facts)
	}

	plain, _ := mgr.ToolPolicy.AfterTool(context.Background(), sess, "write",
		map[string]any{"path": "tests/test_image.py"}, "Wrote 1200 bytes", 1, guidance.ToolResultFacts{})
	if strings.Contains(plain, "EDITORCONFIG_MISMATCH") {
		t.Fatalf("a write with no stated mismatch carried the card:\n%s", plain)
	}
}
