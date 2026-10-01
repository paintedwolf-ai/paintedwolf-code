package search

import "testing"

func TestPathFilterMatchSemantics(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		// Exact full path.
		{"src/pkg/config.py", "src/pkg/config.py", true},
		// Directory prefix at a segment boundary.
		{"src/pkg", "src/pkg/config.py", true},
		{"src/pk", "src/pkg/config.py", false},
		{"src/pkg/", "src/pkg/config.py", true},
		{"/SRC/pkg/", "src/pkg/deep/config.py", true},
		{"src/pkg/", "src/pkg", false},
		{"src/pkg/", "src/pkg-other/config.py", false},
		{"src/*/", "src/pkg/config.py", true},
		{"src/*/", "src/config.py", false},
		// Basename anywhere.
		{"config.py", "src/pkg/config.py", true},
		{"onfig.py", "src/pkg/config.py", false},
		// ASCII case folds, matching SQLite LIKE.
		{"SRC/pkg", "src/pkg/config.py", true},
		{"Config.PY", "src/pkg/config.py", true},
		// Leading slash is ignored on both sides.
		{"/src/pkg", "src/pkg/config.py", true},
		// Wildcards run over the whole path.
		{"src/*/config.py", "src/pkg/config.py", true},
		{"src/*", "src/pkg/config.py", true},
		{"*.py", "src/pkg/config.py", true},
		{"*.go", "src/pkg/config.py", false},
	}
	for _, tc := range cases {
		if got := pathFilterMatch(tc.pattern, tc.path); got != tc.want {
			t.Errorf("pathFilterMatch(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

// TestPathFilterSQLParity locks the store-leg predicate to the code-leg
// matcher: same pattern, same verdict, row by row.
func TestPathFilterSQLParity(t *testing.T) {
	paths := []string{
		"src/pkg/config.py",
		"src/pkg/main.py",
		"src/other/config.py",
		"config.py",
		"SRC/PKG/config.py",
		"vendor/src/pkg/deep.py",
		"src/pkg",
		"src/pkg-other/config.py",
		"src/pkg/deep/config.py",
		"src/under_score/config.py",
		"src/percent%/config.py",
	}
	patterns := []string{
		"src/pkg", "config.py", "src/pkg/config.py", "src/*", "*.py", "SRC/pkg",
		"src/pkg/", "/SRC/pkg/", "src/*/", "src/under_score/", "src/percent%/",
	}
	rows := make([]IndexRow, 0, len(paths))
	for i, path := range paths {
		rows = append(rows, IndexRow{
			ID:      "path-row-" + string(rune('a'+i)),
			Source:  SourceTool,
			HitKind: HitKindTool,
			Path:    path,
			Snippet: "needle in " + path,
		})
	}
	sqlDB := seedStoreRows(t, "sess-path-parity", rows)
	for _, pattern := range patterns {
		hits := runStoreQuery(t, sqlDB, "needle path:"+pattern, MatchFlags{})
		got := map[string]bool{}
		for _, hit := range hits {
			got[hit.Path] = true
		}
		for _, path := range paths {
			want := pathFilterMatch(pattern, path)
			if got[path] != want {
				t.Errorf("pattern %q path %q: store=%v code=%v", pattern, path, got[path], want)
			}
		}
	}
}
