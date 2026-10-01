package structrewrite

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// langCase checks structural rewrites against an independent literal replacement.
type langCase struct {
	lang     string
	src      string
	pattern  string
	fix      string
	wantBind string // expected capture of $A
	oldCall  string // literal call before rewrite
	newCall  string // literal call after rewrite
}

// languageMatrix covers match counts, captures, and rewrites across grammar families.
var languageMatrix = []langCase{
	{
		lang:    "go",
		src:     "package main\n\nfunc main() {\n\tfmt.Println(\"x\")\n}\n",
		pattern: `fmt.Println($A)`, fix: `log.Print($A)`,
		wantBind: `"x"`, oldCall: `fmt.Println("x")`, newCall: `log.Print("x")`,
	},
	{
		lang:    "python",
		src:     "print(\"x\")\n",
		pattern: `print($A)`, fix: `log($A)`,
		wantBind: `"x"`, oldCall: `print("x")`, newCall: `log("x")`,
	},
	{
		lang:    "javascript",
		src:     "foo(1);\n",
		pattern: `foo($A)`, fix: `bar($A)`,
		wantBind: "1", oldCall: "foo(1)", newCall: "bar(1)",
	},
	{
		lang:    "typescript",
		src:     "const x = foo(1);\n",
		pattern: `foo($A)`, fix: `bar($A)`,
		wantBind: "1", oldCall: "foo(1)", newCall: "bar(1)",
	},
	{
		lang:    "tsx",
		src:     "const x = foo(1);\n",
		pattern: `foo($A)`, fix: `bar($A)`,
		wantBind: "1", oldCall: "foo(1)", newCall: "bar(1)",
	},
	{
		lang:    "ruby",
		src:     "foo(1)\n",
		pattern: `foo($A)`, fix: `bar($A)`,
		wantBind: "1", oldCall: "foo(1)", newCall: "bar(1)",
	},
	{
		lang:    "rust",
		src:     "fn m() {\n    foo(1);\n}\n",
		pattern: `foo($A)`, fix: `bar($A)`,
		wantBind: "1", oldCall: "foo(1)", newCall: "bar(1)",
	},
	{
		lang:    "java",
		src:     "class C {\n    void m() {\n        foo(1);\n    }\n}\n",
		pattern: `foo($A)`, fix: `bar($A)`,
		wantBind: "1", oldCall: "foo(1)", newCall: "bar(1)",
	},
	{
		lang:    "c",
		src:     "void f() {\n    foo(1);\n}\n",
		pattern: `foo($A)`, fix: `bar($A)`,
		wantBind: "1", oldCall: "foo(1)", newCall: "bar(1)",
	},
	{
		lang:    "cpp",
		src:     "void f() {\n    foo(1);\n}\n",
		pattern: `foo($A)`, fix: `bar($A)`,
		wantBind: "1", oldCall: "foo(1)", newCall: "bar(1)",
	},
	{
		lang:    "c_sharp",
		src:     "class C {\n    void M() {\n        Foo(1);\n    }\n}\n",
		pattern: `Foo($A)`, fix: `Bar($A)`,
		wantBind: "1", oldCall: "Foo(1)", newCall: "Bar(1)",
	},
	{
		lang:    "php",
		src:     "<?php\nfoo(1);\n",
		pattern: `foo($A)`, fix: `bar($A)`,
		wantBind: "1", oldCall: "foo(1)", newCall: "bar(1)",
	},
	{
		lang:    "lua",
		src:     "foo(1)\n",
		pattern: `foo($A)`, fix: `bar($A)`,
		wantBind: "1", oldCall: "foo(1)", newCall: "bar(1)",
	},
	{
		lang:    "bash",
		src:     "echo hi\n",
		pattern: `echo $A`, fix: `printf $A`,
		wantBind: "hi", oldCall: "echo hi", newCall: "printf hi",
	},
	{
		lang:    "kotlin",
		src:     "fun f() {\n    foo(1)\n}\n",
		pattern: `foo($A)`, fix: `bar($A)`,
		wantBind: "1", oldCall: "foo(1)", newCall: "bar(1)",
	},
	{
		lang:    "scala",
		src:     "object O {\n  def m() {\n    foo(1)\n  }\n}\n",
		pattern: `foo($A)`, fix: `bar($A)`,
		wantBind: "1", oldCall: "foo(1)", newCall: "bar(1)",
	},
	{
		lang:    "swift",
		src:     "func f() {\n    foo(1)\n}\n",
		pattern: `foo($A)`, fix: `bar($A)`,
		wantBind: "1", oldCall: "foo(1)", newCall: "bar(1)",
	},
	{
		lang:    "elixir",
		src:     "foo(1)\n",
		pattern: `foo($A)`, fix: `bar($A)`,
		wantBind: "1", oldCall: "foo(1)", newCall: "bar(1)",
	},
	{
		// Line-oriented: the wrapper supplies the newline that terminates an
		// instruction, without which even a complete one will not parse.
		lang:    "dockerfile",
		src:     "FROM alpine\nRUN apk add curl\n",
		pattern: `RUN $A`, fix: `RUN $A --no-cache`,
		wantBind: "apk add curl", oldCall: "RUN apk add curl", newCall: "RUN apk add curl --no-cache",
	},
}

func TestLanguageMatrixSearchBindRewrite(t *testing.T) {
	testutil.SkipIfShort(t, "tree-sitter language matrix")
	for _, c := range languageMatrix {
		t.Run(c.lang, func(t *testing.T) {
			// Search: exactly one match with the expected capture.
			got := run(t, Request{LangName: c.lang, Source: []byte(c.src), Pattern: c.pattern})
			if len(got.Matches) != 1 {
				t.Fatalf("[%s] want 1 match, got %d: %+v", c.lang, len(got.Matches), got.Matches)
			}
			if bind := got.Matches[0].Bindings["A"]; bind != c.wantBind {
				t.Fatalf("[%s] capture A = %q, want %q", c.lang, bind, c.wantBind)
			}
			// Rewrite: matches an independent string-replacement oracle.
			res := run(t, Request{LangName: c.lang, Source: []byte(c.src), Pattern: c.pattern, Fix: c.fix})
			if !res.Changed {
				t.Fatalf("[%s] expected Changed=true", c.lang)
			}
			want := strings.Replace(c.src, c.oldCall, c.newCall, 1)
			if string(res.Rewritten) != want {
				t.Fatalf("[%s] rewrite mismatch:\n got: %q\nwant: %q", c.lang, res.Rewritten, want)
			}
		})
	}
}

func TestLanguageMatrixCoversAllWrapperFamilies(t *testing.T) {
	// The matrix covers each grammar with a fragment wrapper.
	seen := map[string]bool{}
	for _, c := range languageMatrix {
		seen[c.lang] = true
	}
	for _, c := range languageShapeMatrix {
		seen[c.lang] = true
	}
	for lang := range patternWrappers {
		if !seen[lang] {
			t.Errorf("wrapper grammar %q is covered by neither language matrix", lang)
		}
	}
}

// TestEllipsisAcrossLanguages exercises $$$ sequence capture and reordering in
// several grammar families, proving the ellipsis matcher is language-agnostic.
func TestEllipsisAcrossLanguages(t *testing.T) {
	cases := []struct {
		lang, src, pattern, fix, wantOut string
	}{
		{
			lang:    "go",
			src:     "package main\n\nfunc main() {\n\tswap(a, b)\n}\n",
			pattern: `swap($X, $Y)`, fix: `swap($Y, $X)`,
			wantOut: "package main\n\nfunc main() {\n\tswap(b, a)\n}\n",
		},
		{
			lang:    "python",
			src:     "log(1, 2, 3)\n",
			pattern: `log($$$ARGS)`, fix: `trace($$$ARGS)`,
			wantOut: "trace(1, 2, 3)\n",
		},
		{
			lang:    "typescript",
			src:     "call(a, b, c);\n",
			pattern: `call($$$ARGS)`, fix: `invoke($$$ARGS)`,
			wantOut: "invoke(a, b, c);\n",
		},
		{
			lang:    "rust",
			src:     "fn m() {\n    sum(1, 2, 3);\n}\n",
			pattern: `sum($$$ARGS)`, fix: `total($$$ARGS)`,
			wantOut: "fn m() {\n    total(1, 2, 3);\n}\n",
		},
	}
	for _, c := range cases {
		t.Run(c.lang, func(t *testing.T) {
			res := run(t, Request{LangName: c.lang, Source: []byte(c.src), Pattern: c.pattern, Fix: c.fix})
			if string(res.Rewritten) != c.wantOut {
				t.Fatalf("[%s] got %q want %q", c.lang, res.Rewritten, c.wantOut)
			}
		})
	}
}

// TestMultipleMatchesAcrossLanguages confirms every occurrence is found and
// rewritten, not just the first.
func TestMultipleMatchesAcrossLanguages(t *testing.T) {
	cases := []struct {
		lang, src, pattern, fix string
		wantCount               int
		wantOut                 string
	}{
		{
			lang:    "javascript",
			src:     "foo(1);\nfoo(2);\nfoo(3);\n",
			pattern: `foo($A)`, fix: `bar($A)`, wantCount: 3,
			wantOut: "bar(1);\nbar(2);\nbar(3);\n",
		},
		{
			lang:    "python",
			src:     "print(a)\nprint(b)\n",
			pattern: `print($A)`, fix: `log($A)`, wantCount: 2,
			wantOut: "log(a)\nlog(b)\n",
		},
	}
	for _, c := range cases {
		t.Run(c.lang, func(t *testing.T) {
			res := run(t, Request{LangName: c.lang, Source: []byte(c.src), Pattern: c.pattern, Fix: c.fix})
			if len(res.Matches) != c.wantCount {
				t.Fatalf("[%s] want %d matches, got %d", c.lang, c.wantCount, len(res.Matches))
			}
			if string(res.Rewritten) != c.wantOut {
				t.Fatalf("[%s] got %q want %q", c.lang, res.Rewritten, c.wantOut)
			}
		})
	}
}
