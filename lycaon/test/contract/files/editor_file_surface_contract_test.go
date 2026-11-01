package contract_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/editorturn"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestEditorFileToolSurface_NoExternalExecutionNetworkOrMCP(t *testing.T) {
	t.Parallel()
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)

	var prof *sandbox.ToolProfile
	for i := range profiles {
		if profiles[i].ID == editorturn.ToolProfileID {
			prof = &profiles[i]
			break
		}
	}
	if prof == nil {
		t.Fatalf("tool profile %q not found", editorturn.ToolProfileID)
	}

	forbidden := []string{
		"web_search", "fetch_url",
		"terminal_open", "page_open",
	}
	for _, name := range forbidden {
		if prof.Tools[name] {
			t.Fatalf("%s must not grant %q", editorturn.ToolProfileID, name)
		}
	}
	for name, on := range prof.Tools {
		if !on {
			continue
		}
		if strings.HasPrefix(name, "mcp_") {
			t.Fatalf("%s must not grant MCP tool %q", editorturn.ToolProfileID, name)
		}
		if strings.HasPrefix(name, "delegate_") {
			t.Fatalf("%s must not grant delegate tool %q", editorturn.ToolProfileID, name)
		}
	}

	required := []string{"command", "edit", "replace_lines", "code_rewrite", "summarize"}
	for _, name := range required {
		if !prof.Tools[name] {
			t.Fatalf("%s must grant %q", editorturn.ToolProfileID, name)
		}
	}
}

func TestEditorFileCommandEffectsStayWithinFileOperations(t *testing.T) {
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load editor effect boundary", err)
	for _, profile := range profiles {
		if profile.ID != editorturn.ToolProfileID {
			continue
		}
		for _, c := range []struct {
			class   toolcontract.CommandEffectClass
			allowed bool
		}{
			{toolcontract.CommandReadOnly, true}, {toolcontract.CommandMutation, true},
			{toolcontract.CommandExternalExec, false}, {toolcontract.CommandNetworkEgress, false},
		} {
			required, err := toolcontract.NewCommandEffects(c.class)
			testutil.FailErr(t, "compile effect requirement", err)
			if got := profile.CommandEffects.Allows(required); got != c.allowed {
				t.Errorf("editor effect %s allowed=%v, want %v", c.class, got, c.allowed)
			}
		}
		return
	}
	t.Fatal("editor file profile is missing")
}
