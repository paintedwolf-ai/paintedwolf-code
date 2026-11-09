package contract

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/packboard"
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
				// and names every row by @label — no host paths.
				activeLine := ""
				for _, line := range strings.Split(liveBlock, "\n") {
					if strings.Contains(line, "@"+active.Label) {
						activeLine = line
						break
					}
				}
				if activeLine == "" || !strings.Contains(activeLine, "(session folder)") {
					t.Fatalf("active root line missing (session folder): %q", activeLine)
				}
				if strings.Contains(liveBlock, "(primary)") {
					t.Fatalf("block still marks a primary row:\n%s", liveBlock)
				}
				if strings.Contains(liveBlock, active.Path) {
					t.Fatalf("block leaks the active root host path:\n%s", liveBlock)
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

// AC-WR03-01: one @label workspace folder block at any root count, with no
// host absolute paths in prompts or kicks.
func TestUnifiedFolderBlockGrammarAtEveryRootCount(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	engine := contractcheck.BundledPromptEngineForRoot(t)
	renderer := prompts.NewInjectRenderer(engine)
	ctx := context.Background()

	for _, tc := range []struct {
		name      string
		rootCount int
	}{
		{name: "zero roots", rootCount: 0},
		{name: "single root", rootCount: 1},
		{name: "two roots", rootCount: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var refs []projectroot.RootRef
			activeID := ""
			if tc.rootCount > 0 {
				_, _, r, active := workspaceRootsContractFixture(t, tc.rootCount)
				refs = r
				activeID = active.ID
			}
			vars := map[string]any{"profile_has_command": true}
			prompts.MergeWorkspaceRootsVars(vars, refs, activeID)

			block, err := engine.Render(ctx, "partials/workspace-roots.md", vars)
			contractcheck.FailErr(t, "render workspace-roots", err)
			assertNoHostPaths(t, "workspace-roots", block, refs)
			if tc.rootCount == 0 {
				if !strings.Contains(block, "No folder is attached") {
					t.Fatalf("zero-root block = %q", block)
				}
			} else {
				if !strings.Contains(block, "Workspace folders:") {
					t.Fatalf("block missing folder list: %q", block)
				}
				active, _ := projectroot.ActiveRoot(refs, activeID)
				activeLine := lineContaining(block, "@"+active.Label)
				if activeLine == "" || !strings.Contains(activeLine, "(session folder)") {
					t.Fatalf("active row not marked: %q in\n%s", activeLine, block)
				}
				for _, r := range refs {
					if !strings.Contains(block, "@"+r.Label) {
						t.Fatalf("row missing @%s in\n%s", r.Label, block)
					}
				}
				if strings.Contains(block, "(primary)") {
					t.Fatalf("block still marks a primary row:\n%s", block)
				}
			}

			cmd, err := engine.Render(ctx, "partials/command-surface.md", vars)
			contractcheck.FailErr(t, "render command-surface", err)
			assertNoHostPaths(t, "command-surface", cmd, refs)
			if tc.rootCount > 0 {
				if !strings.Contains(cmd, "Programs start in the session folder") {
					t.Fatalf("command surface start line missing:\n%s", cmd)
				}
				if tc.rootCount > 1 && !strings.Contains(cmd, "`cd` moves between folders by `@label`") {
					t.Fatalf("command surface cd line missing at multi-root:\n%s", cmd)
				}
			}

			// The roots-changed kick names every change and row by @label.
			before := refs
			added := projectroot.RootRef{ID: "extra", Label: "extra", Path: filepath.Join(t.TempDir(), "extra")}
			after := append(append([]projectroot.RootRef{}, refs...), added)
			change := projectroot.DiffRoots(before, after)
			kick, err := engine.RenderKick(ctx, "coordinator-roots-changed", prompts.RootsChangedKickData(change, after, activeID))
			contractcheck.FailErr(t, "render roots-changed kick", err)
			assertNoHostPaths(t, "roots-changed kick", kick, append(refs, added))
			if !strings.Contains(kick, "added @extra.") {
				t.Fatalf("kick change line missing @label:\n%s", kick)
			}
			if tc.rootCount > 0 {
				active, _ := projectroot.ActiveRoot(after, activeID)
				if activeLine := lineContaining(kick, "@"+active.Label); activeLine == "" || !strings.Contains(activeLine, "(session folder)") {
					t.Fatalf("kick active row not marked: %q in\n%s", activeLine, kick)
				}
			}

			// The detach-canceled kick names the folder by @label, and stays
			// grammatical when the host cannot supply one (project delete).
			detach, err := engine.RenderKick(ctx, "coordinator-root-detach-canceled", map[string]any{
				"root_id": "extra", "label": "extra", "workers": []any{}, "overlays": []any{}, "reason": "folder removed",
			})
			contractcheck.FailErr(t, "render detach-canceled kick", err)
			assertNoHostPaths(t, "detach-canceled kick", detach, refs)
			if !strings.Contains(detach, "Folder removed (@extra)") {
				t.Fatalf("detach kick missing @label:\n%s", detach)
			}
			plain, err := engine.RenderKick(ctx, "coordinator-root-detach-canceled", map[string]any{})
			contractcheck.FailErr(t, "render bare detach-canceled kick", err)
			if !strings.Contains(plain, "Folder removed —") {
				t.Fatalf("bare detach kick mis-worded:\n%s", plain)
			}

			// The board orientation names every root by @label at multi-root
			// and stays single-root at one folder.
			orientationRoots := make([]api.BoardOrientationRoot, 0, len(refs))
			for _, r := range refs {
				orientationRoots = append(orientationRoots, api.BoardOrientationRoot{Label: r.Label, IsPrimary: r.IsPrimary, Brief: api.RepoBrief{FileCount: 3}})
			}
			snap := api.BoardSnapshot{Repo: api.RepoBrief{FileCount: 3}, OrientationRoots: orientationRoots}
			board, err := inject.RenderBoardOrientationInject(ctx, renderer, "sess-ac-wr03", snap, packboard.InjectScopeFull, false, false, time.Now().UTC())
			contractcheck.FailErr(t, "render board orientation", err)
			assertNoHostPaths(t, "board orientation", board, refs)
			if tc.rootCount > 1 {
				for _, r := range refs {
					if !strings.Contains(board, "## @"+r.Label) {
						t.Fatalf("board missing @%s header in\n%s", r.Label, board)
					}
				}
			} else if strings.Contains(board, "## @") {
				t.Fatalf("board grew multi-root headers below two roots:\n%s", board)
			}
		})
	}
}

func lineContaining(text, needle string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

// assertNoHostPaths fails when a rendered prompt surfaces a host absolute
// path: any attached root's path, or a removed presentation form.
func assertNoHostPaths(t *testing.T, what, text string, refs []projectroot.RootRef) {
	t.Helper()
	for _, r := range refs {
		if r.Path != "" && strings.Contains(text, r.Path) {
			t.Fatalf("%s leaks the host path of @%s:\n%s", what, r.Label, text)
		}
	}
	for _, banned := range []string{"Project folder:", "Working folder:", "→ /"} {
		if strings.Contains(text, banned) {
			t.Fatalf("%s contains %q:\n%s", what, banned, text)
		}
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
