package contract

import (
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Host injects name only tools the catalog can compile onto the wire. The
// tool-surface partial is built from the profile's list, separate from the
// catalog, so a stale name teaches a call that returns TOOL_NOT_OFFERED.
func TestInjectToolSurfaceNamesOnlyCatalogTools(t *testing.T) {
	t.Parallel()
	profiles := stockToolProfiles(t)

	named := 0
	for _, profile := range profiles {
		surface, err := prompts.LoadAgentToolSurface(profile.ID, nil, nil, prompts.SurfaceTurn{}, profiles)
		contractcheck.FailErr(t, "load inject tool surface for "+profile.ID, err)

		var undeclared []string
		for _, view := range surface.Tools {
			named++
			if !publishableToolName(view.Name) {
				undeclared = append(undeclared, view.Name)
			}
		}
		for _, view := range surface.Requestable {
			named++
			if !publishableToolName(view.Name) {
				undeclared = append(undeclared, "requestable:"+view.Name)
			}
		}
		if len(undeclared) > 0 {
			sort.Strings(undeclared)
			t.Errorf(`tool profile %q injects tool names the catalog does not declare: %s

The agent-tool-surface partial is built from this profile's tool list, so a name
that outlives its catalog entry teaches the model a call that can only come back
unknown. Retire the name from the profile, or declare the tool.`,
				profile.ID, strings.Join(undeclared, ", "))
		}
	}

	if named == 0 {
		t.Fatal("no tool profile named any tool; the contract witnessed nothing")
	}
}

// publishableToolName reports whether compile can put this name on the wire.
// MCP tools are named at runtime and take their contract from the provider's
// generation, so the catalog cannot vouch for them by name.
func publishableToolName(name string) bool {
	if strings.HasPrefix(name, "mcp_") {
		return true
	}
	_, declared := toolcontract.Lookup(name)
	return declared
}

// stockToolProfiles loads the shipped tool profiles the inject surface reads.
func stockToolProfiles(t *testing.T) []sandbox.ToolProfile {
	t.Helper()
	profiles, err := sandbox.LoadToolProfilesWithCatalog(contractcheck.StockCatalog(t))
	contractcheck.FailErr(t, "load stock tool profiles", err)
	if len(profiles) == 0 {
		t.Fatal("no bundled tool profiles loaded")
	}
	return profiles
}
