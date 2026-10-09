package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSourceAnalysisFailureAnnotatesPartialResult(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	sess := &api.Session{ID: "sess-source-analysis"}
	failure := &tsparse.Failure{Reason: "timeout", Language: "swift", TimeoutMS: 5000, ElapsedMS: 5001, SourceBytes: 6236, ParsedBytes: 4000}
	data := failure.Facts("source")
	data["path"], data["parse_failure_count"], data["parse_paths"] = "Level.swift", 1, []string{"Level.swift"}
	raised := guidance.ToolResultFacts{}.WithFeedback(toolrejection.SourceAnalysisUnavailableCode, data, &api.FeedbackSubject{Kind: "path", ID: "Level.swift"})
	out, facts := mgr.appendPostToolGuidance(context.Background(), sess, "grep", map[string]any{"pattern": "spawn($A)", "structural": true}, "partial matches", 1, raised)
	for _, want := range []string{"partial matches", "Code: SOURCE_ANALYSIS_UNAVAILABLE", "Level.swift", "timeout", "5000", "structural=false"} {
		if !strings.Contains(out, want) {
			t.Fatalf("feedback missing %q: %s", want, out)
		}
	}
	if !facts.HasCode(toolrejection.SourceAnalysisUnavailableCode) {
		t.Fatalf("feedback code missing: %+v", facts)
	}
	plain, _ := mgr.appendPostToolGuidance(context.Background(), sess, "grep", map[string]any{"pattern": "spawn"}, "no matches", 1, guidance.ToolResultFacts{})
	if strings.Contains(plain, toolrejection.SourceAnalysisUnavailableCode) {
		t.Fatalf("invented parser failure: %s", plain)
	}
}

func TestSyntaxOverrideFeedbackRecordsExplicitChoice(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	sess := &api.Session{ID: "sess-syntax-override"}
	data := map[string]any{
		"path": "a.swift", "paths": []string{"a.swift"},
		"syntax_override_reason": "the language compiler accepts this file",
	}
	raised := guidance.ToolResultFacts{}.WithFeedback(tools.SyntaxCheckOverriddenCode, data, &api.FeedbackSubject{Kind: "path", ID: "a.swift"})
	raised = raised.WithFeedback(tools.SyntaxCheckOverriddenCode, map[string]any{
		"paths": []string{"b.swift"}, "syntax_override_reason": data["syntax_override_reason"],
	}, &api.FeedbackSubject{Kind: "path", ID: "b.swift"})
	out, facts := mgr.appendPostToolGuidance(context.Background(), sess, "write", map[string]any{"path": "a.swift"}, "write completed", 1, raised)
	for _, want := range []string{"write completed", "Code: SYNTAX_CHECK_OVERRIDDEN", "a.swift", "b.swift", "the language compiler accepts this file"} {
		if !strings.Contains(out, want) {
			t.Fatalf("override feedback missing %q: %s", want, out)
		}
	}
	if !facts.HasCode(tools.SyntaxCheckOverriddenCode) {
		t.Fatalf("override code missing: %+v", facts)
	}
}
