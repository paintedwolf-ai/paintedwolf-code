package prompts_test

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
)

func TestWorkspaceRootRowsMarkActiveRootByID(t *testing.T) {
	primary := projectroot.RootRef{ID: "r1", Label: "app", Path: "/abs/app", IsPrimary: true}
	draft := projectroot.RootRef{ID: "r2", Label: "Draft", Path: "/abs/draft"}

	cases := []struct {
		name        string
		roots       []projectroot.RootRef
		activeID    string
		wantActive  string
		wantPrimary string
	}{
		{name: "single root marks itself active", roots: []projectroot.RootRef{primary}, activeID: "", wantActive: "r1", wantPrimary: "r1"},
		{name: "empty active id falls back to primary", roots: []projectroot.RootRef{primary, draft}, activeID: "", wantActive: "r1", wantPrimary: "r1"},
		{name: "active id marks the active row", roots: []projectroot.RootRef{primary, draft}, activeID: "r2", wantActive: "r2", wantPrimary: "r1"},
		{name: "active id equal to primary", roots: []projectroot.RootRef{primary, draft}, activeID: "r1", wantActive: "r1", wantPrimary: "r1"},
		{name: "unknown active id falls back to primary", roots: []projectroot.RootRef{primary, draft}, activeID: "nope", wantActive: "r1", wantPrimary: "r1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, omitted := prompts.WorkspaceRootRows(tc.roots, tc.activeID)
			if omitted != 0 || len(rows) != len(tc.roots) {
				t.Fatalf("rows=%v omitted=%d", rows, omitted)
			}
			for i, row := range rows {
				id := tc.roots[i].ID
				if row["is_active"] != (id == tc.wantActive) {
					t.Fatalf("row %q is_active=%v want %v", id, row["is_active"], id == tc.wantActive)
				}
				if row["is_primary"] != (id == tc.wantPrimary) {
					t.Fatalf("row %q is_primary=%v", id, row["is_primary"])
				}
			}
		})
	}
}

func TestWorkspaceRootRowsKeepWorkerBranchSessionFolderActive(t *testing.T) {
	// A worker session's WorkspacePath is a branch directory, not a root path:
	// matching by path would leave every row inactive.
	primary := projectroot.RootRef{ID: "r1", Label: "app", Path: "/abs/app", IsPrimary: true}
	draft := projectroot.RootRef{ID: "r2", Label: "Draft", Path: "/abs/draft"}
	rows, omitted := prompts.WorkspaceRootRows([]projectroot.RootRef{primary, draft}, "r2")
	if omitted != 0 {
		t.Fatalf("omitted=%d", omitted)
	}
	if rows[1]["is_active"] != true || rows[0]["is_active"] != false {
		t.Fatalf("active row mismatch: %+v", rows)
	}
}

func TestWorkspaceRootRowsCapsAtCollectionLimit(t *testing.T) {
	roots := make([]projectroot.RootRef, pongoplain.MaxCollectionItems+2)
	for i := range roots {
		roots[i] = projectroot.RootRef{ID: fmt.Sprintf("r%03d", i), Label: fmt.Sprintf("root-%03d", i), Path: fmt.Sprintf("/abs/root-%03d", i)}
	}
	roots[0].IsPrimary = true

	rows, omitted := prompts.WorkspaceRootRows(roots, "r001")
	if len(rows) != pongoplain.MaxCollectionItems {
		t.Fatalf("rows=%d want %d", len(rows), pongoplain.MaxCollectionItems)
	}
	if omitted != 2 {
		t.Fatalf("omitted=%d want 2", omitted)
	}
	if rows[0]["label"] != "root-000" || rows[1]["is_active"] != true {
		t.Fatalf("capped rows lost order or active mark: %+v %+v", rows[0], rows[1])
	}
}

func TestWorkspaceRootRowsEmptyRoots(t *testing.T) {
	rows, omitted := prompts.WorkspaceRootRows(nil, "")
	if rows == nil || len(rows) != 0 || omitted != 0 {
		t.Fatalf("rows=%v omitted=%d", rows, omitted)
	}
}

func TestMergeWorkspaceRootsVarsDelegatesToBuilder(t *testing.T) {
	primary := projectroot.RootRef{ID: "r1", Label: "app", Path: "/abs/app", IsPrimary: true}
	draft := projectroot.RootRef{ID: "r2", Label: "Draft", Path: "/abs/draft"}

	vars := map[string]any{}
	prompts.MergeWorkspaceRootsVars(vars, []projectroot.RootRef{primary, draft}, "r2")
	if vars["root_count"] != 2 {
		t.Fatalf("root_count=%v", vars["root_count"])
	}
	if vars["project_dir"] != "/abs/draft" {
		t.Fatalf("project_dir=%v want active root path", vars["project_dir"])
	}
	if vars["workspace_roots_omitted_count"] != 0 {
		t.Fatalf("omitted=%v", vars["workspace_roots_omitted_count"])
	}
	rows, _ := vars["workspace_roots"].([]map[string]any)
	if len(rows) != 2 || rows[1]["is_active"] != true || rows[0]["is_active"] != false {
		t.Fatalf("rows=%+v", rows)
	}
	if vars["reviews_confined_writes"] != true {
		t.Fatalf("reviews_confined_writes=%v", vars["reviews_confined_writes"])
	}

	empty := map[string]any{}
	prompts.MergeWorkspaceRootsVars(empty, nil, "")
	if empty["root_count"] != 0 || empty["workspace_roots_omitted_count"] != 0 {
		t.Fatalf("empty vars=%+v", empty)
	}
	if _, ok := empty["project_dir"]; ok {
		t.Fatalf("empty roots must not set project_dir: %+v", empty)
	}
}
