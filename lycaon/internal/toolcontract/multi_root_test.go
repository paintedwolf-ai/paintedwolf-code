package toolcontract_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// A gate fails closed on an unclassified tool only when "undeclared" is
// distinguishable from "declared none".
func TestMultiRootOfSeparatesUndeclaredFromNone(t *testing.T) {
	t.Parallel()

	capability, declared := toolcontract.MultiRootOf("read")
	if !declared || capability != toolcontract.MultiRootPathConsume {
		t.Fatalf("read = (%v, %v) want (path, true)", capability, declared)
	}

	capability, declared = toolcontract.MultiRootOf("task")
	if !declared || capability != toolcontract.MultiRootNone {
		t.Fatalf("task = (%v, %v) want (none, true)", capability, declared)
	}

	capability, declared = toolcontract.MultiRootOf("no_such_tool_exists")
	if declared {
		t.Fatalf("an unlisted tool must report undeclared, got (%v, %v)", capability, declared)
	}
	if capability != toolcontract.MultiRootNone {
		t.Fatalf("an undeclared tool must still be safe to read as %v", toolcontract.MultiRootNone)
	}
}

func TestMultiRootOfClassifiesEachKind(t *testing.T) {
	t.Parallel()
	cases := map[string]toolcontract.MultiRootCapability{
		"read":          toolcontract.MultiRootPathConsume,
		"git_status":    toolcontract.MultiRootPathConsume,
		"grep":          toolcontract.MultiRootDiscovery,
		"summarize":     toolcontract.MultiRootDiscovery,
		"command":       toolcontract.MultiRootCommand,
		"verify":        toolcontract.MultiRootCommand,
		"terminal_open": toolcontract.MultiRootCommand,
		"terminal_send": toolcontract.MultiRootNone,
		"web_search":    toolcontract.MultiRootNone,
	}
	for tool, want := range cases {
		got, declared := toolcontract.MultiRootOf(tool)
		if !declared {
			t.Errorf("%s is undeclared", tool)
			continue
		}
		if got != want {
			t.Errorf("%s = %v want %v", tool, got, want)
		}
	}
}

// MCP tool names are provider-supplied and unbounded, so the prefix is their
// declaration — they carry no path surface.
func TestMultiRootOfAcceptsDynamicMCPNames(t *testing.T) {
	t.Parallel()
	capability, declared := toolcontract.MultiRootOf("mcp_someserver_do_a_thing")
	if !declared || capability != toolcontract.MultiRootNone {
		t.Fatalf("mcp tool = (%v, %v) want (none, true)", capability, declared)
	}
}

func TestMultiRootOfNormalizesTheName(t *testing.T) {
	t.Parallel()
	for _, spelling := range []string{"READ", "  read  ", "Read"} {
		capability, declared := toolcontract.MultiRootOf(spelling)
		if !declared || capability != toolcontract.MultiRootPathConsume {
			t.Errorf("%q = (%v, %v) want (path, true)", spelling, capability, declared)
		}
	}
}

func TestMultiRootCapabilityStringsAreStable(t *testing.T) {
	t.Parallel()
	cases := map[toolcontract.MultiRootCapability]string{
		toolcontract.MultiRootNone:        "none",
		toolcontract.MultiRootPathConsume: "path",
		toolcontract.MultiRootDiscovery:   "discovery",
		toolcontract.MultiRootCommand:     "command",
	}
	for capability, want := range cases {
		if got := capability.String(); got != want {
			t.Errorf("%d.String() = %q want %q", capability, got, want)
		}
	}
}

func TestMultiRootDeclaredNamesIsSortedAndNonEmpty(t *testing.T) {
	t.Parallel()
	names := toolcontract.MultiRootDeclaredNames()
	if len(names) == 0 {
		t.Fatal("no tools declared")
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Fatalf("not sorted at %d: %q then %q", i, names[i-1], names[i])
		}
	}
}
