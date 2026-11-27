package toolvocab

import (
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// Wildcard manifest selectors (mcp_*) are not tool names and must never become
// profile_has_<name> template facts; real held tools still do.
func TestSkillRenderVarsSkipsInvalidToolNames(t *testing.T) {
	t.Parallel()
	effects, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation)
	testutil.FailErr(t, "command effects", err)
	reader, err := prompts.NewReader(prompts.HostFacts{
		Profiles: []sandbox.ToolProfile{{
			ID: "worker", Tools: map[string]bool{"command": true, "edit": true, "mcp_*": true}, CommandEffects: effects,
		}},
		RootCount: 1,
	}, prompts.TurnProjection{ProfileID: "worker", Offered: []string{"command", "edit"}, Declared: true})
	testutil.FailErr(t, "reader", err)
	vars, err := prompts.SkillVars(reader)
	testutil.FailErr(t, "skill vars", err)
	if _, ok := vars["profile_has_mcp_*"]; ok {
		t.Fatalf("wildcard family leaked into template facts: %#v", vars)
	}
	for _, name := range []string{"command", "edit"} {
		if vars["profile_has_"+name] != true {
			t.Fatalf("profile_has_%s missing: %#v", name, vars)
		}
	}
	if vars["profile_has_write_tools"] != true {
		t.Fatalf("command mutation still grants write tools: %#v", vars)
	}
}

func TestRenderSkillTextInIsolatesValidationMemoFromGlobalCache(t *testing.T) {
	t.Parallel()
	local := map[string]skillRenderMemo{}
	vars := map[string]any{"profile_has_edit": true}
	varsKey := varsDigestKey(vars)
	text := "{% if profile_has_edit %}edit-enabled{% endif %}"

	first, err := renderSkillTextIn(local, varsKey, text, vars)
	testutil.FailErr(t, "renderSkillTextIn miss", err)
	if first != "edit-enabled" {
		t.Fatalf("renderSkillTextIn = %q, want %q", first, "edit-enabled")
	}
	second, err := renderSkillTextIn(local, varsKey, text, vars)
	testutil.FailErr(t, "renderSkillTextIn hit", err)
	if second != first {
		t.Fatalf("renderSkillTextIn hit = %q, want %q", second, first)
	}
	if _, leaked := skillRenderCache.Load(skillRenderCacheKey(varsKey, text)); leaked {
		t.Fatal("renderSkillTextIn polluted package-global skillRenderCache")
	}
}
