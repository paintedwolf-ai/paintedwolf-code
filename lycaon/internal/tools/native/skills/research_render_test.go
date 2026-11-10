package skills_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	skilltools "github.com/lycaon/lycaon/internal/tools/native/skills"
)

func TestResearchSkillInstructionsFollowReaderCapabilities(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "config", "packs", "painted-wolf", "web-research", "skills", "research-current-information")
	body, err := os.ReadFile(filepath.Join(root, "SKILL.md"))
	testutil.FailErr(t, "read bundled skill", err)
	parsed := skills.Parse("research-current-information", body)
	if len(parsed.Notes) > 0 {
		t.Fatalf("invalid skill: %v", parsed.Notes)
	}
	resource, err := os.ReadFile(filepath.Join(root, "references", "research-routing.md"))
	testutil.FailErr(t, "read routing reference", err)
	for _, state := range []struct {
		name                             string
		search, fetch, command, mutation bool
		roots                            int
	}{
		{"no web tools", false, false, false, false, 0},
		{"search only", true, false, false, false, 0},
		{"reader only", false, true, false, false, 0},
		{"research worker", true, true, false, false, 0},
		{"read-only command", true, true, true, false, 1},
		{"no project", true, true, true, true, 0},
		{"build surface", true, true, true, true, 1},
	} {
		t.Run(state.name, func(t *testing.T) {
			reader := skilltools.SkillsReadTool{TemplateVars: func(context.Context, tools.ToolContext) map[string]any {
				return map[string]any{
					"profile_has_web_search": state.search, "profile_has_fetch_url": state.fetch, "profile_has_command": state.command, "profile_command_mutation": state.mutation, "root_count": state.roots,
				}
			}}
			sk := parsed.Skill
			for _, source := range []string{sk.Body, string(resource)} {
				sk.Body = source
				rendered, err := reader.RenderBody(t.Context(), tools.ToolContext{}, sk)
				testutil.FailErr(t, "render research instructions", err)
				for _, capability := range []struct {
					name    string
					enabled bool
				}{{"web_search", state.search}, {"fetch_url", state.fetch}} {
					if strings.Contains(rendered, "`"+capability.name+"`") != capability.enabled {
						t.Fatalf("%s instructions disagree with capabilities: %s", capability.name, rendered)
					}
				}
				if source == string(resource) && strings.Contains(rendered, "`command`") != (state.command && state.mutation && state.roots > 0) {
					t.Fatalf("download instruction disagrees with surface: %s", rendered)
				}
			}
		})
	}
}
