package linuxsandbox

import "testing"

func TestCreationFloorsHoldProtectedLocationsByName(t *testing.T) {
	t.Parallel()
	f := newCreationFloors(Filesystem{
		WriteRoots:    []string{"/p", "/p/.paintedwolf/rules/granted"},
		WriteFiles:    []string{"/p/sub/AGENTS.md"},
		ReadOnly:      []string{"/p/.paintedwolf/mcp.yaml", "/p/.paintedwolf/rules", "/p/.agents/skills"},
		Hidden:        []string{"/p/.secrets"},
		Restored:      []string{"/p/.paintedwolf/rules/granted"},
		ReadOnlyNames: []NamedFloor{{Below: "/p", Name: "AGENTS.md"}},
	})
	for _, tc := range []struct {
		kind creation
		path string
		want bool
	}{
		{createEntry, "/p/.paintedwolf/mcp.yaml", true},
		{createEntry, "/p/.PaintedWolf/MCP.yaml", true},
		{createEntry, "/p/.paintedwolf/notes.txt", false},
		{createDirectory, "/p/.paintedwolf", false},
		{createDirectory, "/p/.paintedwolf/rules", false},
		{createEntry, "/p/.paintedwolf/rules/x.yaml", true},
		{createEntry, "/p/.paintedwolf/rules/granted/x.yaml", false},
		{createDirectory, "/p/.agents/skills", true},
		{createEntry, "/p/.agents/skills/s/SKILL.md", true},
		{createEntry, "/p/.secrets", true},
		{createEntry, "/p/AGENTS.md", true},
		{createEntry, "/p/deep/er/agents.MD", true},
		{createEntry, "/p/sub/AGENTS.md", false},
		{createEntry, "/elsewhere/AGENTS.md", false},
		{createEntry, "/p/src/main.go", false},
		{createEntry, "/p/new-top-level", false},
		{renameSource, "/p/.paintedwolf", false},
		{renameSource, "/p/.paintedwolf/mcp.yaml", true},
		{renameSource, "/p/doc/AGENTS.md", true},
		{renameTarget, "/p/.paintedwolf", true},
		{renameTarget, "/p/.agents", true},
		{renameTarget, "/p", true},
		{renameTarget, "/p/src2", false},
		{renameTarget, "/p/x/AGENTS.md", true},
	} {
		if got := f.refuses(tc.kind, tc.path); got != tc.want {
			t.Errorf("refuses(%d, %q) = %v, want %v", tc.kind, tc.path, got, tc.want)
		}
	}
}

func TestAtOrUnderFoldComparesWholeComponents(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		path, root string
		want       bool
	}{
		{"/a/b", "/a", true},
		{"/A/b", "/a", true},
		{"/ab", "/a", false},
		{"/a", "/a/b", false},
		{"/x", "/", true},
		{"/a/b/", "/a/b", true},
	} {
		if got := atOrUnderFold(tc.path, tc.root); got != tc.want {
			t.Errorf("atOrUnderFold(%q, %q) = %v, want %v", tc.path, tc.root, got, tc.want)
		}
	}
}
