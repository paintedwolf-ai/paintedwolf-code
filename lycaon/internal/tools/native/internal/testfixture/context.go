package testfixture

import (
	"encoding/json"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
	"github.com/lycaon/lycaon/pkg/api"
)

func Boundary(t *testing.T) *sandbox.Boundary {
	t.Helper()
	return sandbox.NewBoundary(sandbox.Config{
		ProjectRootRequired: true,
		RejectSymlinkEscape: true,
	}, []sandbox.ToolProfile{
		{
			ID:    toolprofiles.DefaultToolProfileID,
			Tools: map[string]bool{"read": true, "write": true, "edit": true, "replace_lines": true, "grep": true, "find": true, "stat": true, "wc": true, "list_dir": true, "chmod": true, "delete": true, "survey_repo": true},
		},
		{
			ID:    "coordinator",
			Tools: map[string]bool{"read": true, "write": true, "edit": true, "replace_lines": true, "grep": true, "find": true, "stat": true, "wc": true, "list_dir": true, "chmod": true, "delete": true, "survey_repo": true},
		},
	})
}

func Context(dir string) tools.ToolContext {
	roots := []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}}
	// Publish a below-threshold count for open-root tool tests.
	return tools.ToolContext{
		Roots:               roots,
		ActiveRootID:        "r1",
		SourceWorkspaceKind: api.SourceWorkspaceKindProject,
		Agent:               toolprofiles.DefaultToolProfileID,
		SessionID:           "test-session",
		RepoFileCount:       100,
		RepoFileCountKnown:  true,
	}
}

func AgentContext(dir, agent string) tools.ToolContext {
	ctx := Context(dir)
	ctx.Agent = agent
	return ctx
}

func SurveyContent(t *testing.T, out string) string {
	t.Helper()
	var wrap struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(out), &wrap); err == nil && wrap.Content != "" {
		return wrap.Content
	}
	return out
}

func GrepMatches(t *testing.T, out string) []map[string]any {
	t.Helper()
	body := SurveyContent(t, out)
	var wrap struct {
		Matches []map[string]any `json:"matches"`
	}
	if err := json.Unmarshal([]byte(body), &wrap); err != nil {
		t.Fatalf("decode grep: %v", err)
	}
	if wrap.Matches == nil {
		return []map[string]any{}
	}
	return wrap.Matches
}

func AssertReceipt(t *testing.T, out string) {
	t.Helper()
	if _, ok := surveyreceipt.Parse(out); !ok {
		t.Fatalf("missing survey receipt in %q", out)
	}
}
