package contract

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

// AC-WR02-01: live prompt assembly reads the session's roots from the project
// registry and marks the active root by ID (even when the session path is a
// worker branch directory), lists every root by label up to the prompt
// collection limit and counts the rest as omitted.
func TestWorkspaceRootsPromptMarksSessionFolderAndOmittedCount(t *testing.T) {
	engine := contractcheck.BundledPromptEngineForRoot(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name      string
		rootCount int
		// inMemory names the roots without attaching folders: the cap and the
		// omitted count depend on the references, not on the directories.
		inMemory bool
	}{
		{name: "single root", rootCount: 1},
		{name: "two roots", rootCount: 2},
		{name: "over collection cap", rootCount: pongoplain.MaxCollectionItems + 7, inMemory: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := workspaceRootsContractFixture
			if tc.inMemory {
				fixture = workspaceRootsReferenceFixture
			}
			reg, projectID, refs, active := fixture(t, tc.rootCount)
			sess := &api.Session{
				ProjectID:       projectID,
				WorkspaceRootID: active.ID,
				// A worker session's WorkspacePath is a branch directory, not a
				// root path: is_active must come from the root ID.
				WorkspacePath: filepath.Join(t.TempDir(), "worker-branches", "root-abc"),
			}
			workspace := scope.New(nil)
			workspace.SetProjects(reg)
			rows, rootCount, activePath, omitted := workspace.PromptRootRows(ctx, sess)

			listed := min(tc.rootCount, pongoplain.MaxCollectionItems)
			if rootCount != tc.rootCount || len(rows) != listed || omitted != tc.rootCount-listed || activePath != active.Path {
				t.Fatalf("roots=%d rows=%d omitted=%d active=%q, want %d, %d, %d, %q",
					rootCount, len(rows), omitted, activePath, tc.rootCount, listed, tc.rootCount-listed, active.Path)
			}
			for i, row := range rows {
				wantActive := refs[i].ID == active.ID
				if row["label"] != refs[i].Label || row["is_active"] != wantActive || row["is_primary"] != (i == 0) {
					t.Fatalf("row %d = %v, want label %q active %v primary %v", i, row, refs[i].Label, wantActive, i == 0)
				}
			}

			vars := map[string]any{"project_dir": activePath, "root_count": rootCount, "workspace_roots": rows, "workspace_roots_omitted_count": omitted}
			block, err := engine.Render(ctx, "partials/workspace-roots.md", vars)
			contractcheck.FailErr(t, "render workspace-roots", err)
			assertNoHostPaths(t, "workspace-roots", block, refs)
			if tc.rootCount > 1 {
				for _, r := range refs[:listed] {
					if !strings.Contains(block, "@"+r.Label) {
						t.Fatalf("row missing @%s in\n%s", r.Label, block)
					}
				}
				if line := lineContaining(block, "@"+active.Label); line == "" || !strings.Contains(line, "(session folder)") {
					t.Fatalf("active root line missing (session folder): %q", line)
				}
				if strings.Contains(block, "(primary)") {
					t.Fatalf("block still marks a primary row:\n%s", block)
				}
			}
			if omitted > 0 && !strings.Contains(block, strconv.Itoa(omitted)) {
				t.Fatalf("block does not state the omitted count %d:\n%s", omitted, block)
			}
		})
	}
}

// AC-WR03-01: one @label workspace folder block at any root count, with no
// host absolute paths in prompts or kicks.
func TestUnifiedFolderBlockGrammarAtEveryRootCount(t *testing.T) {
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
			vars := map[string]any{"profile_has_command": true, "root_count": len(refs)}
			prompts.MergeWorkspaceRootsVars(vars, refs, activeID)

			block, err := engine.Render(ctx, "partials/workspace-roots.md", vars)
			contractcheck.FailErr(t, "render workspace-roots", err)
			assertNoHostPaths(t, "workspace-roots", block, refs)
			if tc.rootCount > 0 {
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

			// The roots-changed kick names every change and row by @label.
			before := refs
			added := projectroot.RootRef{ID: "extra", Label: "extra", Path: filepath.Join(t.TempDir(), "extra")}
			after := append(append([]projectroot.RootRef{}, refs...), added)
			change := projectroot.DiffRoots(before, after)
			kick, err := engine.RenderKick(ctx, "coordinator-roots-changed", prompts.RootsChangedKickData(change, after, activeID))
			contractcheck.FailErr(t, "render roots-changed kick", err)
			assertNoHostPaths(t, "roots-changed kick", kick, append(refs, added))
			if !strings.Contains(kick, "@extra") {
				t.Fatalf("kick does not name the added folder by @label:\n%s", kick)
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
			if !strings.Contains(detach, "@extra") {
				t.Fatalf("detach kick does not name the folder by @label:\n%s", detach)
			}
			plain, err := engine.RenderKick(ctx, "coordinator-root-detach-canceled", map[string]any{})
			contractcheck.FailErr(t, "render bare detach-canceled kick", err)
			assertNoHostPaths(t, "bare detach-canceled kick", plain, refs)

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

// workspaceRootLabel names root i: the first is primary, the second ("Draft")
// is the session's active root. With a single root that root is "Draft" and
// is both primary and active.
func workspaceRootLabel(i, rootCount int) string {
	switch {
	case rootCount == 1:
		return "Draft"
	case i == 0:
		return "app"
	case i == 1:
		return "Draft"
	}
	return fmt.Sprintf("root-%03d", i)
}

// workspaceRootsContractFixture attaches rootCount real folders to one project
// through the registry.
func workspaceRootsContractFixture(t *testing.T, rootCount int) (scope.Projects, string, []projectroot.RootRef, projectroot.RootRef) {
	t.Helper()
	base := t.TempDir()
	roots := make([]project.AttachRootParams, rootCount)
	for i := range roots {
		dir := filepath.Join(base, fmt.Sprintf("root-%03d", i))
		contractcheck.FailErr(t, fmt.Sprintf("mkdir root %d", i), os.MkdirAll(dir, 0o755))
		roots[i] = project.AttachRootParams{Path: dir, Label: workspaceRootLabel(i, rootCount)}
	}
	reg := project.NewMemoryRegistry()
	created, err := reg.Create(context.Background(), project.CreateParams{Roots: roots})
	contractcheck.FailErr(t, "create project", err)
	refs, active := workspaceRootsActive(t, project.RootRefsFrom(created), rootCount)
	return reg, created.ID, refs, active
}

// workspaceRootsReferenceFixture names rootCount roots of one project that no
// directory backs.
func workspaceRootsReferenceFixture(t *testing.T, rootCount int) (scope.Projects, string, []projectroot.RootRef, projectroot.RootRef) {
	t.Helper()
	base := t.TempDir()
	p := &project.Project{ID: "workspace-roots-project", Roots: make([]project.Root, rootCount)}
	for i := range p.Roots {
		p.Roots[i] = project.Root{
			ID: fmt.Sprintf("root-%03d", i), ProjectID: p.ID, IsPrimary: i == 0,
			Path: filepath.Join(base, fmt.Sprintf("root-%03d", i)), Label: workspaceRootLabel(i, rootCount),
		}
	}
	refs, active := workspaceRootsActive(t, project.RootRefsFrom(p), rootCount)
	return referenceProjects{p.ID: p}, p.ID, refs, active
}

// workspaceRootsActive checks every root was admitted and picks the active one.
func workspaceRootsActive(t *testing.T, refs []projectroot.RootRef, rootCount int) ([]projectroot.RootRef, projectroot.RootRef) {
	t.Helper()
	if len(refs) != rootCount {
		t.Fatalf("attached %d roots, want %d", len(refs), rootCount)
	}
	if rootCount > 1 {
		return refs, refs[1]
	}
	return refs, refs[0]
}

type referenceProjects map[string]*project.Project

func (p referenceProjects) Get(_ context.Context, id string) (*project.Project, error) {
	return p[id], nil
}
