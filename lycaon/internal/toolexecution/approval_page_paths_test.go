package toolexecution

import (
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPageApprovalUsesMountedDirectory(t *testing.T) {
	for _, tool := range []string{"page_open", "capture_page", "measure_page"} {
		for _, entry := range []string{"", "index.html", "/index.html", "/nested/"} {
			got := filesFromArgs(tool, map[string]any{"project_dir": "site", "path": entry})
			if !reflect.DeepEqual(got, []string{"site"}) {
				t.Errorf("%s entry %q: files = %v, want mounted directory", tool, entry, got)
			}
		}
	}
}

func TestPageApprovalDoesNotTreatURLPathsAsFiles(t *testing.T) {
	for _, tool := range []string{"page_open", "capture_page", "measure_page"} {
		for _, args := range []map[string]any{
			{"url": "http://localhost:3000/index.html"},
			{"path": "/index.html"},
			{"project_dir": "  ", "path": "/index.html"},
			{"page_handle": "page#1"},
		} {
			if got := filesFromArgs(tool, args); len(got) != 0 {
				t.Errorf("%s args %v: URL path became file target %v", tool, args, got)
			}
		}
	}
	if got := filesFromArgs("read", map[string]any{"path": "/index.html"}); !reflect.DeepEqual(got, []string{"/index.html"}) {
		t.Errorf("ordinary file path changed: %v", got)
	}
}

func TestPageApprovalResolvesWorkspaceDirectory(t *testing.T) {
	root, branch, outside := t.TempDir(), t.TempDir(), t.TempDir()
	tc := tools.ToolContext{Source: tools.InvocationSource{
		Roots: []projectroot.RootRef{{ID: "root", Label: "folder", Path: root, IsPrimary: true}}, ActiveRootID: "root",
	}}
	for _, worker := range []bool{false, true} {
		expected := root
		if worker {
			tc.Source.WorkerBranchRoot = branch
			expected = branch
		}
		for _, tool := range []string{"page_open", "capture_page", "measure_page"} {
			for _, tt := range []struct{ dir, want string }{
				{".", fspath.CanonicalPath(expected)},
				{"site", fspath.CanonicalPath(filepath.Join(expected, "site"))},
				{"@folder/site", fspath.CanonicalPath(filepath.Join(expected, "site"))},
				{outside, fspath.CanonicalPath(outside)},
				{"@missing/site", ""},
			} {
				got := ResolvedApprovalFiles(tool, map[string]any{"project_dir": tt.dir, "path": "/index.html"}, tc)
				if !reflect.DeepEqual(got, []string{tt.want}) {
					t.Errorf("worker=%t tool=%s dir=%q: got %v want %q", worker, tool, tt.dir, got, tt.want)
				}
			}
		}
	}
}
