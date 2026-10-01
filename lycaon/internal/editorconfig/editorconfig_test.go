package editorconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestParseReadsRootAndSections(t *testing.T) {
	f := Parse("", "\nroot = true\n\n[*]\nindent_style = space\nindent_size = 2\n\n[*.go]\r\nINDENT_STYLE = tab\n; comment\n# comment\n")
	if !f.Root {
		t.Fatal("root = true in the preamble should mark the file as root")
	}
	if len(f.Sections) != 3 || *f.Sections[1].Pattern != "*" || f.Sections[2].Pairs["indent_style"] != "tab" {
		t.Fatalf("sections = %+v", f.Sections)
	}
}

func TestMatchPattern(t *testing.T) {
	cases := []struct {
		pattern, rel string
		want         bool
	}{
		{"*.ts", "src/foo.ts", true},
		{"*.ts", "src/foo.js", false},
		{"lib/**.js", "lib/a/b.js", true},
		{"**/vendor/*.go", "vendor/x.go", true},
		{"**/vendor/*.go", "a/vendor/x.go", true},
		{"{package.json,.travis.yml}", "package.json", true},
		{"file{1..3}.txt", "file2.txt", true},
		{"file{1..3}.txt", "file4.txt", false},
		{"/vendor.go", "vendor.go", true},
		{"/vendor.go", "a/vendor.go", false},
		{"/*.ts", "a/foo.ts", false},
		{"{s1}", "{s1}", true},
		{"{s1}", "s1", false},
		{`foo\*.txt`, "foo*.txt", true},
		{`foo\*.txt`, "foobar.txt", false},
		{"[!a]b.c", "xb.c", true},
		{"[!a]b.c", "ab.c", false},
		{"[a-]x", "-x", true},
		{"[a-]x", "bx", false},
		{"?.md", "a/b.md", true},
		{"a?b", "a/b", false},
	}
	for _, tc := range cases {
		if got := MatchPattern(tc.pattern, tc.rel); got != tc.want {
			t.Errorf("MatchPattern(%q, %q) = %v, want %v", tc.pattern, tc.rel, got, tc.want)
		}
	}
}

func TestResolveNearerConfigWinsAndUnsetClears(t *testing.T) {
	root := Parse("", "[*]\nindent_style = space\nindent_size = 2\ntrim_trailing_whitespace = true\ninsert_final_newline = true\n")
	nested := Parse("lycaon", "[*.go]\nindent_style = tab\nindent_size = 4\ninsert_final_newline = unset\n")
	got := Resolve("lycaon/main.go", []File{root, nested})
	if got.IndentStyle != IndentTab || got.IndentSize != 4 || got.TrimTrailingWhitespace == nil || !*got.TrimTrailingWhitespace || got.InsertFinalNewline != nil {
		t.Fatalf("Resolve = %+v", got)
	}
	if other := Resolve("docs/a.md", []File{root, nested}); other.IndentStyle != IndentSpace || other.IndentSize != 2 {
		t.Fatalf("a file outside the nested section should keep the root pairs, got %+v", other)
	}
}

func TestMergeIgnoresUnrecognizedValues(t *testing.T) {
	got := Properties{}.merge(map[string]string{
		"indent_style": "space", "indent_size": "tab", "tab_width": "0",
		"end_of_line": "cr", "trim_trailing_whitespace": "maybe", "charset": "ebcdic",
	})
	want := Properties{IndentStyle: IndentSpace, IndentSizeTab: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merge = %+v, want %+v", got, want)
	}
}

func TestIndentWidth(t *testing.T) {
	cases := []struct {
		props Properties
		want  int
	}{
		{Properties{}, 0},
		{Properties{TabWidth: 8}, 0},
		{Properties{IndentStyle: IndentSpace}, 4},
		{Properties{IndentStyle: IndentSpace, IndentSize: 2}, 2},
		{Properties{IndentStyle: IndentTab, TabWidth: 8}, 8},
		{Properties{IndentSizeTab: true, TabWidth: 3}, 3},
		{Properties{IndentSize: 2}, 2},
	}
	for _, tc := range cases {
		if got := tc.props.IndentWidth(); got != tc.want {
			t.Errorf("%+v.IndentWidth() = %d, want %d", tc.props, got, tc.want)
		}
	}
}

func TestSearchDirs(t *testing.T) {
	if got := SearchDirs("a/b/c.ts"); !reflect.DeepEqual(got, []string{"a/b", "a", ""}) {
		t.Fatalf("SearchDirs = %q", got)
	}
	if got := SearchDirs("c.ts"); !reflect.DeepEqual(got, []string{""}) {
		t.Fatalf("SearchDirs root file = %q", got)
	}
}

func TestLoadStopsAtRootTrue(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			testutil.FailErr(t, "mkdir", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			testutil.FailErr(t, "write config", err)
		}
	}
	write(".editorconfig", "[*]\ntrim_trailing_whitespace = true\nend_of_line = crlf\n")
	write("pkg/.editorconfig", "root = true\n[*.py]\nindent_style = space\n")
	write("pkg/sub/x.py", "")

	got, err := Load(dir, "pkg/sub/x.py")
	if err != nil {
		testutil.FailErr(t, "load", err)
	}
	if got.IndentStyle != IndentSpace || got.TrimTrailingWhitespace != nil || got.EndOfLine != "" {
		t.Fatalf("root = true in pkg should hide the project config, got %+v", got)
	}
	outside, err := Load(dir, "x.py")
	if err != nil {
		testutil.FailErr(t, "load root file", err)
	}
	if outside.EndOfLine != EndOfLineCRLF {
		t.Fatalf("root file should read the project config, got %+v", outside)
	}
	none, err := Load(dir, "missing/dir/y.py")
	if err != nil {
		testutil.FailErr(t, "load under missing dir", err)
	}
	if none.EndOfLine != EndOfLineCRLF {
		t.Fatalf("a missing directory should fall through to the project config, got %+v", none)
	}
}

func TestLoadRefusesOversizedConfig(t *testing.T) {
	dir := t.TempDir()
	body := make([]byte, MaxFileBytes+1)
	if err := os.WriteFile(filepath.Join(dir, FileName), body, 0o644); err != nil {
		testutil.FailErr(t, "write config", err)
	}
	if _, err := Load(dir, "a.go"); err == nil {
		t.Fatal("an oversized config should fail to load")
	}
}
