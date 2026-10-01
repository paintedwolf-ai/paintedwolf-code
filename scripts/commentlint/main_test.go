package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func TestParseComments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		file string
		src  string
		want string
	}{
		{name: "go", file: "fixture.go", src: "package fixture\n// explain the branch\nvar value = 1\n", want: "// explain the branch"},
		{name: "typescript", file: "fixture.ts", src: "const value = 1; // preserve ordering\n", want: "// preserve ordering"},
		{name: "python", file: "fixture.py", src: "value = 1  # preserve ordering\n", want: "# preserve ordering"},
		{name: "diff", file: "fixture.patch", src: "# patch note\n+# added\n-# removed\n # context\n", want: "# patch note"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			grammar := grammars.DetectLanguage(test.file)
			if grammar == nil || grammar.Language == nil {
				t.Fatalf("no grammar for %s", test.file)
			}
			language := grammar.Language()
			parser := gotreesitter.NewParser(language)
			comments, timedOut, err := extractComments(syntaxParser{language: language, parser: parser}, grammar.Name, []byte(test.src), time.Second)
			if err != nil {
				t.Fatalf("extract comments: %v", err)
			}
			if timedOut {
				t.Fatal("extract comments timed out")
			}
			if len(comments) != 1 || comments[0].Text != test.want {
				t.Fatalf("comments = %#v, want %q", comments, test.want)
			}
		})
	}
}

func TestInspect(t *testing.T) {
	t.Parallel()
	opts := options{
		maxLineLength:           20,
		workMarkersRequireIssue: true,
		issuePattern:            regexp.MustCompile(`#[0-9]+`),
		localHomePath:           regexp.MustCompile(`/Users/example`),
		forbidden:               []namedPattern{{name: "internal-name", pattern: regexp.MustCompile(`secret-name`)}},
	}
	item := comment{Language: "go", Kind: "comment", StartRow: 3, StartCol: 1, Text: "// HACK explain /Users/example secret-name beyond the limit"}
	findings := inspect("fixture.go", item, opts)
	if len(findings) != 4 {
		t.Fatalf("got %d findings, want 4: %#v", len(findings), findings)
	}
	item.Text += " commentlint:allow work-marker"
	findings = inspect("fixture.go", item, opts)
	for _, finding := range findings {
		if finding.Rule == "work-marker" {
			t.Fatal("work marker suppression was ignored")
		}
	}
}

func TestWorkMarkersAreExplicit(t *testing.T) {
	t.Parallel()
	if workMarkerPattern.MatchString("PHP and Hack") {
		t.Fatal("ordinary title case text matched")
	}
	if !workMarkerPattern.MatchString("// HACK #123") {
		t.Fatal("uppercase marker was missed")
	}
}

func TestLexicalComments(t *testing.T) {
	t.Parallel()
	src := "const url = 'https://example.com/a/long/path'; // line\n/* block\ncomment */\n"
	comments, supported := lexicalComments("typescript", []byte(src))
	if !supported {
		t.Fatal("typescript lexer is unavailable")
	}
	if len(comments) != 2 {
		t.Fatalf("comments = %#v", comments)
	}
	if comments[0].Text != "// line" || comments[0].StartRow != 1 || comments[0].StartCol != 48 {
		t.Fatalf("line comment = %#v", comments[0])
	}
	if comments[1].Text != "/* block\ncomment */" || comments[1].StartRow != 2 {
		t.Fatalf("block comment = %#v", comments[1])
	}
}

func TestCommentlessGrammarHasNoComments(t *testing.T) {
	t.Parallel()
	comments, supported := lexicalComments("json", []byte(`{"note":"TODO /Users/example"}`))
	if !supported || len(comments) != 0 {
		t.Fatalf("comments = %#v", comments)
	}
}

func TestHashCommentAfterCode(t *testing.T) {
	t.Parallel()
	comments, supported := lexicalComments("python", []byte("value = 1# HACK\n"))
	if !supported {
		t.Fatal("python lexer is unavailable")
	}
	if len(comments) != 1 || comments[0].Text != "# HACK" {
		t.Fatalf("comments = %#v", comments)
	}
}

func TestNestedBlockComment(t *testing.T) {
	t.Parallel()
	comments, supported := lexicalComments("rust", []byte("/* outer /* inner */ HACK */\n"))
	if !supported || len(comments) != 1 || comments[0].Text != "/* outer /* inner */ HACK */" {
		t.Fatalf("comments = %#v", comments)
	}
}

func TestUnknownLanguageRequiresSyntaxValidation(t *testing.T) {
	t.Parallel()
	comments, supported := lexicalComments("some_unknown_lang", []byte("// HACK\n"))
	if supported || len(comments) != 0 {
		t.Fatalf("comments = %#v, supported = %v", comments, supported)
	}
}

func TestPotentialFindingUsesSyntaxValidation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	opts := options{
		root: root, maxLineLength: 120, workMarkersRequireIssue: true,
		issuePattern: regexp.MustCompile(`#[0-9]+`), parseTimeout: time.Second,
	}
	languages := languageRegistry{values: map[string]*gotreesitter.Language{}}
	tests := []struct {
		name         string
		source       string
		wantFindings int
	}{
		{name: "template text", source: "const value = `// HACK`;\n"},
		{name: "comment", source: "const value = 1; // HACK\n", wantFindings: 1},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(root, test.name+".ts")
			if err := os.WriteFile(path, []byte(test.source), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			result := scanFile(opts, fileJob{path: path, rel: filepath.Base(path)}, map[string]syntaxParser{}, &languages)
			if result.err != nil {
				t.Fatalf("scan file: %v", result.err)
			}
			if !result.validated {
				t.Fatal("potential finding was not syntax-validated")
			}
			if len(result.findings) != test.wantFindings {
				t.Fatalf("findings = %#v", result.findings)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	data := `{"max_line_length":88,"work_markers_require_issue":false,"excludes":["generated"]}`
	if err := os.WriteFile(filepath.Join(root, "commentlint.json"), []byte(data), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := loadConfig(root, "commentlint.json")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.MaxLineLength == nil || *cfg.MaxLineLength != 88 {
		t.Fatalf("max line length = %v", cfg.MaxLineLength)
	}
	if cfg.WorkMarkersRequireIssue == nil || *cfg.WorkMarkersRequireIssue {
		t.Fatalf("work markers require issue = %v", cfg.WorkMarkersRequireIssue)
	}
	if len(cfg.Excludes) != 1 || cfg.Excludes[0] != "generated" {
		t.Fatalf("excludes = %#v", cfg.Excludes)
	}
}

func TestCompileForbiddenRejectsDuplicateNames(t *testing.T) {
	t.Parallel()
	if _, err := compileForbidden([]string{"name=one", "name=two"}); err == nil {
		t.Fatal("duplicate rule was accepted")
	}
}

func TestDiffCommentsExcludeChangedAndContextLines(t *testing.T) {
	src := "# patch note\n+# added source\n-# deleted source\n # context\n@@ -1 +1 @@\n"
	comments, supported := lexicalComments("diff", []byte(src))
	if !supported || len(comments) != 1 || comments[0].Text != "# patch note" || comments[0].StartRow != 1 {
		t.Fatalf("diff comments = %#v, supported = %v", comments, supported)
	}
}

func TestLargeDiffReportsCommentsWithoutSyntaxParsing(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "large.patch")
	src := "# TODO patch note\n" + strings.Repeat("@@ -1 +1 @@\n-# old\n+# new\n", 10000)
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write diff: %v", err)
	}
	opts := options{root: root, maxLineLength: 120, workMarkersRequireIssue: true,
		issuePattern: regexp.MustCompile(`#[0-9]+`), parseTimeout: time.Nanosecond}
	result := scanFile(opts, fileJob{path: path, rel: "large.patch"}, map[string]syntaxParser{},
		&languageRegistry{values: map[string]*gotreesitter.Language{}})
	if result.err != nil || result.timedOut || result.validated || len(result.findings) != 1 {
		t.Fatalf("diff scan = %+v", result)
	}
}
