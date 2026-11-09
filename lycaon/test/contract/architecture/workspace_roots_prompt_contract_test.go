package contract

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// AC-WR02-01: live prompt assembly and prompt_dump build workspace root rows
// with one builder, so the (session folder) marker follows the active root ID
// (even when the session path is a worker branch directory) and the row list
// caps at the prompt collection limit with an omitted count.
func TestWorkspaceRootsPromptMarksSessionFolderAndOmittedCount(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	engine := contractcheck.BundledPromptEngineForRoot(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name      string
		rootCount int
	}{
		{name: "single root", rootCount: 1},
		{name: "two roots", rootCount: 2},
		{name: "over collection cap", rootCount: pongoplain.MaxCollectionItems + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, projectID, refs, active := workspaceRootsContractFixture(t, tc.rootCount)
			sess := &api.Session{
				ProjectID:       projectID,
				WorkspaceRootID: active.ID,
				// A worker session's WorkspacePath is a branch directory, not a
				// root path: is_active must come from the root ID.
				WorkspacePath: filepath.Join(t.TempDir(), "worker-branches", "root-abc"),
			}

			// Live assembly path: scope.PromptRootRows feeds the turn vars that
			// prefix_projection/turn_orientation assemble (see
			// assembly.mergeWorkspaceRootTurnVars).
			workspace := scope.New(nil)
			workspace.SetProjects(reg)
			rows, rootCount, activePath, omitted := workspace.PromptRootRows(ctx, sess)
			liveVars := map[string]any{"project_dir": sess.WorkspacePath}
			liveVars["root_count"] = rootCount
			liveVars["workspace_roots"] = rows
			liveVars["workspace_roots_omitted_count"] = omitted
			if activePath != "" {
				liveVars["project_dir"] = activePath
			}
			liveBlock, err := engine.Render(ctx, "partials/workspace-roots.md", liveVars)
			contractcheck.FailErr(t, "render live workspace-roots", err)

			// prompt_dump path: prompts.MergeWorkspaceRootsVars.
			dumpVars := map[string]any{"project_dir": sess.WorkspacePath}
			prompts.MergeWorkspaceRootsVars(dumpVars, refs, active.ID)
			dumpBlock, err := engine.Render(ctx, "partials/workspace-roots.md", dumpVars)
			contractcheck.FailErr(t, "render prompt_dump workspace-roots", err)

			if liveBlock != dumpBlock {
				t.Fatalf("live assembly and prompt_dump disagree:\nlive:\n%s\ndump:\n%s", liveBlock, dumpBlock)
			}

			// Exactly the active row carries is_active, at every root count.
			activeRows := 0
			for _, row := range rows {
				if row["is_active"] == true {
					activeRows++
					if row["path"] != active.Path {
						t.Fatalf("is_active on %v, want the active root %q", row["path"], active.Path)
					}
				}
			}
			if activeRows != 1 {
				t.Fatalf("is_active rows=%d want 1", activeRows)
			}
			if liveVars["workspace_roots_omitted_count"] != tc.rootCount-len(rows) {
				t.Fatalf("omitted=%v want %d", liveVars["workspace_roots_omitted_count"], tc.rootCount-len(rows))
			}

			if tc.rootCount > 1 {
				// The rendered list marks the active row with (session folder)
				// and the primary row with (primary).
				markLine := func(marker string) string {
					for _, line := range strings.Split(liveBlock, "\n") {
						if strings.Contains(line, marker) {
							return line
						}
					}
					return ""
				}
				activeLine := markLine(active.Path)
				if activeLine == "" || !strings.Contains(activeLine, "(session folder)") {
					t.Fatalf("active root line missing (session folder): %q", activeLine)
				}
				primaryLine := markLine("(primary)")
				if primaryLine == "" || strings.Contains(primaryLine, "(session folder)") {
					t.Fatalf("primary line mis-marked: %q", primaryLine)
				}
			}
			if tc.rootCount > pongoplain.MaxCollectionItems {
				want := fmt.Sprintf("%d additional folders are omitted from this prompt view", tc.rootCount-pongoplain.MaxCollectionItems)
				if !strings.Contains(liveBlock, want) {
					t.Fatalf("omitted count line missing: want %q in\n%s", want, liveBlock)
				}
			}
		})
	}
}

// workspaceRootsContractFixture attaches rootCount folders to one project: the
// first is primary, the second ("Draft") is the session's active root. With a
// single root that root is "Draft" and is both primary and active.
func workspaceRootsContractFixture(t *testing.T, rootCount int) (*project.MemoryRegistry, string, []projectroot.RootRef, projectroot.RootRef) {
	t.Helper()
	base := t.TempDir()
	roots := make([]project.AttachRootParams, rootCount)
	for i := range roots {
		dir := filepath.Join(base, fmt.Sprintf("root-%03d", i))
		contractcheck.FailErr(t, fmt.Sprintf("mkdir root %d", i), os.MkdirAll(dir, 0o755))
		label := fmt.Sprintf("root-%03d", i)
		switch {
		case rootCount == 1:
			label = "Draft"
		case i == 0:
			label = "app"
		case i == 1:
			label = "Draft"
		}
		roots[i] = project.AttachRootParams{Path: dir, Label: label}
	}
	reg := project.NewMemoryRegistry()
	created, err := reg.Create(context.Background(), project.CreateParams{Roots: roots})
	contractcheck.FailErr(t, "create project", err)
	refs := project.RootRefsFrom(created)
	if len(refs) != rootCount {
		t.Fatalf("attached %d roots, want %d", len(refs), rootCount)
	}
	active := refs[0]
	if rootCount > 1 {
		active = refs[1]
	}
	return reg, created.ID, refs, active
}
