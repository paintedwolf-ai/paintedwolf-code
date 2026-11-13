package toolvocab

import "testing"

// Wildcard manifest selectors (mcp_*) are not tool names and must never become
// profile_has_<name> template facts; real held tools still do.
func TestSkillRenderVarsSkipsInvalidToolNames(t *testing.T) {
	t.Parallel()
	vars := skillRenderVars(map[string]bool{"command": true, "edit": true, "mcp_*": true, ReachCommandMutation: true}, false)
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
