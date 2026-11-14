package contract

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/tools"
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

// AC-WR03-02: no config template renders a host absolute path — workspace
// roots, scratch, and worker workspaces are addressed by @label.
func TestMultiRootASTNoHostPathTemplateVars(t *testing.T) {
	t.Parallel()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon")
	scan, err := scanMultiRootAST(root)
	contractcheck.FailErr(t, "scanMultiRootAST", err)
	if len(scan.HostPathTemplateVars) > 0 {
		t.Fatalf("host path template vars in config templates:\n%s", strings.Join(scan.HostPathTemplateVars, "\n"))
	}
	// The detector itself must fire: feed it the removed patterns and the
	// surviving display-path exceptions.
	out := &multiRootASTScan{}
	scanMultiRootTemplateData("config/packs/painted-wolf/platform/guidance/coordinator-roots-changed.md",
		"{{ r.path }} {{ c.old_path }} {{ new_primary.path }} {{ active_root.path }} {{ root.path }} {{ project_dir }} {{ session_scratch_dir }} {{ worker_workspace_path }} {{ path }}", out)
	if len(out.HostPathTemplateVars) != 9 {
		t.Fatalf("detector missed host path vars:\n%s", strings.Join(out.HostPathTemplateVars, "\n"))
	}
	out = &multiRootASTScan{}
	scanMultiRootTemplateData("config/packs/painted-wolf/platform/guidance/curator/user.md", "{{ c.path }}", out)
	if len(out.HostPathTemplateVars) != 0 {
		t.Fatalf("curator evidence path must stay allowed:\n%s", strings.Join(out.HostPathTemplateVars, "\n"))
	}
	out = &multiRootASTScan{}
	scanMultiRootTemplateData("config/packs/painted-wolf/platform/guidance/editor/inline-edit.md", "{{ path }}", out)
	if len(out.HostPathTemplateVars) != 0 {
		t.Fatalf("editor document path is not a host path:\n%s", strings.Join(out.HostPathTemplateVars, "\n"))
	}
	out = &multiRootASTScan{}
	scanMultiRootTemplateData("config/packs/painted-wolf/platform/shared/partials/command-surface.md", "{% if session_scratch_dir %}", out)
	if len(out.HostPathTemplateVars) != 0 {
		t.Fatalf("statement guards never render a value:\n%s", strings.Join(out.HostPathTemplateVars, "\n"))
	}
}

// The cwd argument selects an attached root.
func TestMultiRootDisclosureCommandCwdIsTheOnlyRootSelector(t *testing.T) {
	exec := toolfixture.ContractToolExecutor(t)
	for _, rootCount := range []int{0, 1, 2, 3} {
		metas := tools.ListToolsForProfile(context.Background(), exec.Metadata, platform.ToolFilter{
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
