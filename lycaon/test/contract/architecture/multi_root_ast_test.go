package contract

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

func TestMultiRootASTNoSingleRootJoinAntiPatterns(t *testing.T) {
	t.Parallel()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon")
	scan, err := scanMultiRootAST(root)
	contractcheck.FailErr(t, "scanMultiRootAST", err)
	if len(scan.SingleRootJoins) > 0 {
		t.Fatalf("single-root join anti-patterns:\n%s", strings.Join(scan.SingleRootJoins, "\n"))
	}
	if len(scan.ActiveRootPathJoins) > 0 {
		t.Fatalf("ActiveRootPath join anti-patterns:\n%s", strings.Join(scan.ActiveRootPathJoins, "\n"))
	}
}

func TestMultiRootASTAddressingCopySingleSource(t *testing.T) {
	t.Parallel()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon")
	scan, err := scanMultiRootAST(root)
	contractcheck.FailErr(t, "scanMultiRootAST", err)
	if len(scan.DuplicateAddressing) > 0 {
		t.Fatalf("multi-root addressing copy outside SSOT partials:\n%s", strings.Join(scan.DuplicateAddressing, "\n"))
	}
}

func TestMultiRootDisclosureSingleRootWorkspaceGolden(t *testing.T) {
	t.Parallel()
	engine := contractcheck.BundledPromptEngineForRoot(t)
	vars := map[string]any{}
	prompts.MergeWorkspaceRootsVars(vars, []projectroot.RootRef{
		{ID: "r1", Label: "lycaon", Path: "/tmp/lycaon", IsPrimary: true},
	}, "/tmp/lycaon")
	out, err := engine.Render(context.Background(), "partials/workspace-roots.md", vars)
	contractcheck.FailErr(t, "render workspace-roots", err)
	if !strings.Contains(out, "Workspace: /tmp/lycaon") {
		t.Fatalf("output = %q", out)
	}
	for _, banned := range []string{"@", "spans ", "root=", "folders"} {
		if strings.Contains(out, banned) {
			t.Fatalf("single-root output leaks multi-root vocab %q: %s", banned, out)
		}
	}
}

// Relative paths resolve under the marked session folder.
func TestMultiRootDisclosureSessionFolderMarked(t *testing.T) {
	t.Parallel()
	engine := contractcheck.BundledPromptEngineForRoot(t)
	roots := []projectroot.RootRef{
		{ID: "r1", Label: "lycaon", Path: "/tmp/lycaon", IsPrimary: true},
		{ID: "r2", Label: "lycaon-den", Path: "/tmp/lycaon-den"},
	}
	for _, tc := range []struct {
		activePath string
	}{
		{activePath: "/tmp/lycaon"},
		{activePath: "/tmp/lycaon-den"},
	} {
		vars := map[string]any{}
		prompts.MergeWorkspaceRootsVars(vars, roots, tc.activePath)
		out, err := engine.Render(context.Background(), "partials/workspace-roots.md", vars)
		contractcheck.FailErr(t, "render workspace-roots", err)
		if !strings.Contains(out, "(session folder)") {
			t.Fatalf("active %s: no session-folder marker: %s", tc.activePath, out)
		}
		marked := ""
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "(session folder)") {
				marked = line
				break
			}
		}
		if !strings.Contains(marked, tc.activePath) {
			t.Fatalf("active %s: marker on wrong row %q", tc.activePath, marked)
		}
	}
}

func TestMultiRootDisclosureZeroRootWorkspaceGolden(t *testing.T) {
	t.Parallel()
	engine := contractcheck.BundledPromptEngineForRoot(t)
	vars := map[string]any{}
	prompts.MergeWorkspaceRootsVars(vars, nil, "")
	out, err := engine.Render(context.Background(), "partials/workspace-roots.md", vars)
	contractcheck.FailErr(t, "render workspace-roots zero", err)
	if strings.Contains(out, "Workspace:") {
		t.Fatalf("zero-root output must not include Working directory line: %s", out)
	}
	if !strings.Contains(out, "No folder is attached") {
		t.Fatalf("zero-root output = %q", out)
	}
}

func TestMultiRootDisclosureSingleRootToolSchemasQuiet(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir", err)
	for name, meta := range schemas.Tools {
		cap := multiRootCapabilityOf(name)
		if cap == toolcontract.MultiRootNone || cap == toolcontract.MultiRootCommand {
			continue
		}
		desc := strings.ToLower(meta.Description)
		for _, needle := range []string{"@label", "root=", "multi-root", "multi root", "spans n folder"} {
			if strings.Contains(desc, needle) {
				t.Errorf("tool %q description leaks multi-root vocab at 1 root: %q", name, meta.Description)
			}
		}
	}
}

// The cwd argument selects an attached root.
func TestMultiRootDisclosureCommandCwdIsTheOnlyRootSelector(t *testing.T) {
	exec := toolfixture.ContractToolExecutor(t)
	for _, rootCount := range []int{0, 1, 2, 3} {
		metas := tools.ListToolsForProfile(context.Background(), exec, platform.ToolFilter{
			ProfileID:        "implement",
			ProjectRootCount: rootCount,
		})
		for _, meta := range metas {
			if meta.Name != "command" {
				continue
			}
			props, _ := meta.ArgsSchema["properties"].(map[string]any)
			if props == nil {
				t.Fatalf("command schema properties missing at root count %d", rootCount)
			}
			if _, ok := props["root"]; ok {
				t.Fatalf("command schema advertises root at root count %d — args validate against the canonical schema, so an injected property is a reject trap", rootCount)
			}
			cwd, ok := props["cwd"].(map[string]any)
			if !ok {
				t.Fatalf("command schema missing cwd at root count %d", rootCount)
			}
			desc, _ := cwd["description"].(string)
			if !strings.Contains(desc, "@label") {
				t.Fatalf("command cwd description must teach @label addressing, got %q", desc)
			}
		}
	}
}
