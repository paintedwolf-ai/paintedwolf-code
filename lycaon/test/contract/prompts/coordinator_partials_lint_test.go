package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// coordinatorCoreAllowedOutputVars is the closed set of output variables
// coordinator-core.md may interpolate.
var coordinatorCoreAllowedOutputVars = map[string]bool{
	"max_author_progress_lines": true,
	"max_progress_label_chars":  true,
	"surface_card":              true,
	"units":                     true,
}

func TestCoordinatorCoreVarWhitelist(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "agents", "prompts", "coordinator-core.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read core: %v", err)
	}
	text := string(raw)
	if strings.Contains(text, "execution_mode_entered") || strings.Contains(text, "execution_mode_left") {
		t.Fatal("coordinator-core must not reference entered/left vars")
	}
	if strings.Contains(text, "greenfield_build") {
		t.Fatal("coordinator-core must not reference greenfield_build")
	}
	for _, m := range regexp.MustCompile(`\{\{\s*([a-z_]+)`).FindAllStringSubmatch(text, -1) {
		name := m[1]
		if !coordinatorCoreAllowedOutputVars[name] {
			t.Fatalf("coordinator-core references unlisted output var {{ %s }}; add it to coordinatorCoreAllowedOutputVars if intentional", name)
		}
	}
	for _, m := range regexp.MustCompile(`\{%\s*if\s+(?:not\s+)?([a-z_]+)`).FindAllStringSubmatch(text, -1) {
		name := m[1]
		if strings.HasPrefix(name, "profile_has_") {
			continue
		}
		switch name {
		case "execution_mode", "has_file_tools", "can_orient", "can_spawn_web_research", "can_spawn_workers", "web_search_enabled", "agent_skills", "agent_host_resources", "surface_card":
		default:
			t.Fatalf("coordinator-core {%% if %%} may only branch on execution_mode or capability flags, found %q", name)
		}
	}
}
