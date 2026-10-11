package extensionadmin

import (
	"testing"

	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/projectsource"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSourceReadPublishesContributionLanguage(t *testing.T) {
	for _, tc := range []struct{ path, language string }{
		{"main.go", "go"}, {"src/main.py", "python"}, {"src/main.ts", "typescript"},
		{"README.md", ""}, {"unknown.fixture", ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			got := sourceapi.ToProjectSourceReadDTO(&projectsource.SourceReadResult{Path: tc.path}, "workspace", wire.SourceWorkspaceKindProject)
			if got.Language != tc.language {
				t.Errorf("source language = %q, want %q", got.Language, tc.language)
			}
			state := hostFactState{invokeCtx: wire.CommandInvokeContext{Path: got.Path}}
			if got.Language != "" && !hostFactBinders["editor_language"](state, got.Language) {
				t.Errorf("source language %q cannot satisfy the host fact for %s", got.Language, got.Path)
			}
		})
	}
}
