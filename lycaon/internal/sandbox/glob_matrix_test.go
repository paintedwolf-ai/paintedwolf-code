package sandbox

import (
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/pathglob"
)

func TestEntryGlobPOSIXOperators(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
		reason  string
	}{
		// Single-char wildcard
		{"a?.go", "ab.go", true, "?-matches one char"},
		{"a?.go", "abc.go", false, "? does not match two chars"},
		{"a?.go", "a.go", false, "? requires one char, not zero"},

		// Character class
		{"f[oi]o.go", "foo.go", true, "[oi] matches o"},
		{"f[oi]o.go", "fio.go", true, "[oi] matches i"},
		{"f[oi]o.go", "fao.go", false, "[oi] rejects a"},

		// POSIX [!chars] negation
		{"f[!a]o.go", "fao.go", false, "[!a] excludes a"},
		{"f[!a]o.go", "foo.go", true, "[!a] matches o (not a)"},
		{"f[!a]o.go", "f!o.go", true, "[!a] matches !"},
		{"v[!0-9].go", "vx.go", true, "[!0-9] matches non-digit"},
		{"v[!0-9].go", "v3.go", false, "[!0-9] excludes digit"},

		// Character range
		{"v[0-9].go", "v3.go", true, "[0-9] range matches digit"},
		{"v[0-9].go", "vx.go", false, "[0-9] range rejects letter"},

		// Basename-only when pattern has no slash or **
		{"main.go", "lycaon/cmd/main.go", true, "basename match"},
		{"main.go", "lycaon/main.go.bak", false, "basename equality strict"},

		// Hidden / dotfile basename
		{".env", "config/.env", true, "dotfile basename"},

		// Path-mode when pattern contains /
		{"cmd/main.go", "cmd/main.go", true, "exact path match"},
		{"cmd/main.go", "lycaon/cmd/main.go", false, "path-mode requires anchored prefix"},

		// Path-mode with **
		{"**/main.go", "lycaon/cmd/main.go", true, "** prefix matches any depth"},
		{"**/main.go", "main.go", true, "** prefix matches root"},

		// Empty pattern → match all
		{"", "anything.go", true, "empty pattern is match-all"},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			glob, err := CompileEntryGlob(tc.pattern)
			testutil.FailErr(t, "compile entry glob", err)
			got := glob.Match(tc.path)
			if got != tc.want {
				t.Fatalf("glob %q matches %q = %v want %v", tc.pattern, tc.path, got, tc.want)
			}
		})
	}
}

func TestPathGlobMatchLeadingSlashNotNormalized(t *testing.T) {
	// Scope matching normalizes the leading ./; path matching is exact.
	if pathglob.Match("lycaon/internal/x.go", "./lycaon/internal/x.go") {
		t.Fatal("pathglob.Match is exact: no ./ trim. Use matchAnyGlob for the trimmed form.")
	}
	if !matchAnyGlob([]string{"lycaon/internal/x.go"}, "./lycaon/internal/x.go") {
		t.Fatal("matchAnyGlob trims leading ./ before matching")
	}
	// A trailing slash does not imply recursion.
	if pathglob.Match("lycaon/", "lycaon/internal/x.go") {
		t.Fatal("trailing slash should not implicitly mean recurse")
	}
}

func TestPathGlobMatchDoubleStarEdgeCases(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"**", "any/depth/file.go", true},
		{"**", "", true},

		// Trailing /** requires at least one descendant segment
		{"docs/**", "docs", false},
		{"docs/**", "docs/x.md", true},
		{"docs/**", "docs/sub/x.md", true},
		{"docs/**", "other/docs/x.md", false},

		// Middle **/x/** form
		{"**/.git/**", "lycaon/.git/HEAD", true},
		{"**/.git/**", "lycaon/foo.go", false},
		{"**/.git/**", ".git/HEAD", true},

		// Double-star prefixes match file patterns at any depth.
		{"**/*.go", "lycaon/internal/x.go", true},
		{"**/*.go", "lycaon/internal/notes.txt", false},
		{"**/*_test.go", "lycaon/internal/x_test.go", true},
		{"**/*_test.go", "lycaon/internal/x.go", false},
		{"**/x_test.go", "lycaon/internal/x_test.go", true},
		{"**/foo_*.go", "pkg/foo_bar.go", true},
		{"**/foo_*.go", "pkg/bar_foo.go", false},

		// Multi-segment prefix + ** + suffix
		{"lycaon/**/x.go", "lycaon/internal/foo/x.go", true},
		{"lycaon/**/x.go", "other/internal/x.go", false},
	}
	for _, tc := range cases {
		t.Run(tc.pattern+" vs "+tc.path, func(t *testing.T) {
			if got := pathglob.Match(tc.pattern, tc.path); got != tc.want {
				t.Fatalf("pathglob.Match(%q,%q) = %v want %v", tc.pattern, tc.path, got, tc.want)
			}
		})
	}
}

func TestMatchAnyGlobMatchesEmptyPatternList(t *testing.T) {
	if !matchAnyGlob(nil, "x.go") {
		t.Fatal("nil patterns slice must be match-all")
	}
	if !matchAnyGlob([]string{}, "x.go") {
		t.Fatal("empty patterns slice must be match-all")
	}
}

func TestMatchesScopeGlobEmptyIsNoMatch(t *testing.T) {
	if matchesScopeGlob(nil, "x.go") {
		t.Fatal("nil enforcement patterns must not match")
	}
	if matchesScopeGlob([]string{}, "x.go") {
		t.Fatal("empty enforcement patterns must not match")
	}
}

func TestMatchesScopeGlobEnforcementNeverAllowsOnEmptyWrite(t *testing.T) {
	scope := PathScope{}
	for _, path := range []string{"src/a.go", settingsoverlay.DirName() + "/blueprints/x.md"} {
		if err := CheckWriteInScope(scope, "empty", path); err == nil {
			t.Fatalf("CheckWriteInScope must deny %q when Write/Allow/Deny are empty", path)
		}
	}
}

func TestMatchAnyGlobShortCircuitsOnFirstHit(t *testing.T) {
	if !matchAnyGlob([]string{"never-matches/*", "lycaon/**"}, "lycaon/x.go") {
		t.Fatal("second pattern should still match")
	}
	if matchAnyGlob([]string{"never-matches/*", "still-no-match/**"}, "lycaon/x.go") {
		t.Fatal("when no pattern matches, result is false")
	}
}

func TestMatchToolPatternWildcards(t *testing.T) {
	if !matchToolPattern("git_*", "git_status") {
		t.Fatal("trailing-* prefix should match")
	}
	if !matchToolPattern("read", "read") {
		t.Fatal("exact match must work")
	}
	if matchToolPattern("git_*", "read") {
		t.Fatal("trailing-* must not match unrelated tool")
	}
	// Tool patterns support only a trailing wildcard.
	if matchToolPattern("git_*_remote", "git_show_remote") {
		t.Fatal("embedded wildcard matched a tool name")
	}
}
