package toolexecution

import (
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPageApprovalUsesMountedDirectory(t *testing.T) {
	for _, tool := range []string{"page_open", "capture_page", "measure_page"} {
		for _, tc := range []struct {
			entry string
			want  []string
		}{
			{"", []string{"site", "site/index.html"}},
			{"index.html", []string{"site", "site/index.html"}},
			{"/index.html", []string{"site", "site/index.html"}},
			{"/nested/", []string{"site", "site/nested", "site/nested/index.html"}},
		} {
			entry := tc.entry
			got := filesFromArgs(tool, map[string]any{"project_dir": "site", "path": entry})
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s entry %q: files = %v, want %v", tool, entry, got, tc.want)
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
				want := []string{tt.want, ""}
				if tt.want != "" {
					want[1] = fspath.CanonicalPath(filepath.Join(tt.want, "index.html"))
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("worker=%t tool=%s dir=%q: got %v want %q", worker, tool, tt.dir, got, tt.want)
				}
			}
		}
	}
}

func TestPageApprovalRetainsEntryDenials(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		entries []string
	}{
		{"private.html", []string{"private.html", "/private.html"}},
		{"index.html", []string{"", "/nested/"}},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			configtest.Overlay(t, map[config.Rel]string{config.SecurityApprovals: "rules:\n  - category: path\n    pattern: " + tc.pattern + "\n    effect: deny\n"})
			store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
			testutil.FailErr(t, "load approval store", err)
			gate := settings.NewRuleApprovalGate(store, settings.NoSources())
			for _, tool := range []string{"page_open", "capture_page", "measure_page"} {
				for _, entry := range tc.entries {
					action := proposedActionFromPolicy(platform.PolicyContext{ToolName: tool, ToolArgs: map[string]any{"project_dir": ".", "path": entry}})
					result, err := gate.Evaluate(t.Context(), action)
					testutil.FailErr(t, "evaluate static entry policy", err)
					if !result.Denied || result.DenyCode != "APPROVAL_RULE_DENIED" {
						t.Fatalf("%s entry %q bypassed path deny: %+v", tool, entry, result)
					}
				}
			}
		})
	}
}
