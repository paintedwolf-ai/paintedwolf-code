package detectionpack

import (
	"fmt"
	"strings"
	"testing"
)

func TestProjectToolArgsScalarsAndNesting(t *testing.T) {
	t.Parallel()
	got := projectToolArgs(map[string]any{
		"query":     "aws credentials",
		"limit":     float64(10),
		"recursive": true,
		"nested":    map[string]any{"url": "https://example.test"},
		"list":      []any{"one", "two"},
	})
	want := []string{
		"limit=10",
		"list=one",
		"list=two",
		"nested.url=https://example.test",
		"query=aws credentials",
		"recursive=true",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("ProjectToolArgs = %v, want %v", got, want)
	}
}

// Long values are dropped, not truncated: a prefix would place the head of every
// written file into a matchable field.
func TestProjectToolArgsDropsOversizeValues(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("x", maxToolArgBytes+1)
	got := projectToolArgs(map[string]any{"path": "src/main.go", "content": body})
	if len(got) != 1 || got[0] != "path=src/main.go" {
		t.Fatalf("ProjectToolArgs = %v, want only the short path arg", got)
	}
	atCap := strings.Repeat("x", maxToolArgBytes)
	if got := projectToolArgs(map[string]any{"content": atCap}); len(got) != 1 {
		t.Fatalf("value exactly at cap must project, got %d entries", len(got))
	}
}

func TestProjectToolArgsBounded(t *testing.T) {
	t.Parallel()
	args := make(map[string]any, maxToolArgs*2)
	for i := range maxToolArgs * 2 {
		args[fmt.Sprintf("key%02d", i)] = "value"
	}
	if got := projectToolArgs(args); len(got) > maxToolArgs {
		t.Fatalf("len = %d, want <= %d", len(got), maxToolArgs)
	}
}

func TestProjectToolArgsDepthBounded(t *testing.T) {
	t.Parallel()
	deep := map[string]any{"a": map[string]any{"b": map[string]any{"c": map[string]any{"d": "buried"}}}}
	for _, entry := range projectToolArgs(deep) {
		if strings.Contains(entry, "buried") {
			t.Fatalf("value past the depth cap projected: %q", entry)
		}
	}
}

// An argv-less tool must still reach rules with matchable content.
func TestToolArgRuleMatchesArgvLessTool(t *testing.T) {
	t.Parallel()
	ev := NewEvent(ActionObservation{
		Tool:        "web_search",
		ToolArgs:    projectToolArgs(map[string]any{"query": "instance metadata 169.254.169.254"}),
		TargetFiles: []string{"src/main.go"},
	})
	if ev.CommandLine != "" {
		t.Fatalf("CommandLine = %q, want empty for an argv-less tool", ev.CommandLine)
	}
	rule := mustRule(t, `
title: Metadata endpoint named in a tool argument
description: A tool argument names a cloud metadata endpoint.
id: 6f1f4d3a-4f5f-4a3e-9f1a-2b7c9d0e5a11
level: high
logsource:
  product: lycaon
  service: tool_exec
detection:
  selection:
    Tool: 'web_search'
    ToolArg|contains: '169.254.169.254'
  condition: selection
`)
	if !rule.Matches(ev) {
		t.Fatalf("ToolArg rule did not match %+v", ev.ToolArg)
	}

	targeted := mustRule(t, `
title: Target file named
description: The action names a Go source file.
id: 6f1f4d3a-4f5f-4a3e-9f1a-2b7c9d0e5a12
level: high
logsource:
  product: lycaon
  service: tool_exec
detection:
  selection:
    TargetFile|endswith: '.go'
  condition: selection
`)
	if !targeted.Matches(ev) {
		t.Fatalf("TargetFile rule did not match %+v", ev.TargetFile)
	}
}
