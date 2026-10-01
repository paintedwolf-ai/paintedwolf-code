package gtsqueries

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// querySamples exercises definition capture for each curated grammar.
var querySamples = map[string]string{
	"markdown":        "# Title\n\n## Section\n",
	"markdown_inline": "$$x+1$$\n",
	"html":            "<h1>Hi</h1>\n",
	"xml":             "<note><body>x</body></note>\n",
	"dtd":             "<!ELEMENT note (#PCDATA)>\n",
	"rst":             "Title\n=====\n\nbody\n",
	"org":             "* Heading\nbody\n",
	"norg":            "* Heading\n",
	"djot":            "# Heading\n",
	"typst":           "= Heading\n",
	"vimdoc":          "Intro *my-tag*\n\nSECTION\n",
	"bibtex":          "@article{key1,\n  title = {x}\n}\n",
	"jsdoc":           "/**\n * @param foo bar\n */\n",
	"yaml":            "key: value\nother: 1\n",
	"toml":            "a = 1\n[table]\nb = 2\n",
	"json":            "{\"a\": 1, \"b\": 2}\n",
	"json5":           "{a: 1, b: 2}\n",
	"jsonnet":         "{ foo: 1, bar: 2 }\n",
	"ini":             "[section]\nkey = value\n",
	"properties":      "my.key=value\n",
	"editorconfig":    "[*.go]\nindent_style = tab\n",
	"git_config":      "[core]\n\teditor = vim\n",
	"ssh_config":      "Host example\n  HostName 1.2.3.4\n",
	"desktop":         "[Desktop Entry]\nName=App\n",
	"hyprlang":        "general {\n  border = 1\n}\n",
	"kdl":             "title \"Hello\"\n",
	"kconfig":         "config FOO\n\tbool \"foo\"\n",
	"corn":            "{ foo = 1 bar = 2 }\n",
	"cpon":            "{\"foo\": 1}\n",
	"ron":             "Struct(field: 1)\n",
	"pkl":             "foo = 1\nclass Bar {}\n",
	"nickel":          "{ foo = 1 }\n",
	"cue":             "foo: 1\nbar: 2\n",
	"hurl":            "GET https://example.com\n",
	"http":            "GET https://example.com\n",
	"foam":            "FoamFile\n{\n    version 2.0;\n}\n",
	"textproto":       "name: \"x\"\n",
	"csv":             "a,b,c\n1,2,3\n",
	"pem":             "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n",
	"dockerfile":      "FROM alpine AS build\n",
	"earthfile":       "build:\n    FROM alpine\n",
	"hcl":             "resource \"aws_s3\" \"b\" {\n}\n",
	"cmake":           "function(my_func)\nendfunction()\n",
	"make":            "target: dep\n\techo hi\nVAR = 1\n",
	"meson":           "project_name = 'x'\n",
	"ninja":           "rule cc\n  command = gcc\n",
	"just":            "build:\n\techo hi\n",
	"nginx":           "worker_processes 1;\n",
	"caddy":           "(mysnippet) {\n  header foo bar\n}\n",
	"gomod":           "module example.com/foo\n\ngo 1.21\n",
	"linkerscript":    "SECTIONS {\n  .text : { *(.text) }\n}\n",
	"bash":            "foo() { echo hi; }\nx=1\n",
	"fish":            "function foo\n  echo hi\nend\n",
	"awk":             "function foo() { print 1 }\n",
	"nushell":         "def foo [] { 1 }\n",
	"powershell":      "function Foo { 1 }\n",
	"tcl":             "proc foo {} { return 1 }\n",
	"perl":            "sub foo { 1 }\npackage Bar;\n",
	"groovy":          "def foo() { 1 }\nclass Bar {}\n",
	"php":             "<?php\nfunction foo() {}\nclass Bar {}\n",
	"twig":            "{% macro foo() %}{% endmacro %}\n",
	"liquid":          "{% assign foo = 1 %}\n",
	"haskell":         "module Main where\nfoo :: Int\nfoo = 1\ndata Bar = Bar\n",
	"ocaml":           "let foo = 1\ntype bar = int\n",
	"elm":             "module Main exposing (..)\nfoo = 1\ntype Bar = Bar\n",
	"purescript":      "module Main where\nfoo = 1\n",
	"fennel":          "(local foo 1)\n",
	"clojure":         "(defn foo [] 1)\n(def bar 2)\n",
	"commonlisp":      "(defun foo () 1)\n",
	"elisp":           "(defun foo () 1)\n",
	"janet":           "(defn foo [] 1)\n(def bar 2)\n",
	"erlang":          "-module(foo).\nbar() -> 1.\n",
	"forth":           ": foo 1 + ;\n",
	"pascal":          "function Foo: Integer;\nbegin\nend;\n",
	"cobol":           "       IDENTIFICATION DIVISION.\n       PROGRAM-ID. HELLO.\n       PROCEDURE DIVISION.\n       MAIN-PARA.\n           STOP RUN.\n",
	"brightscript":    "function Foo() as void\nend function\n",
	"firrtl":          "circuit Top :\n  module Top :\n",
	"verilog":         "module m;\nendmodule\n",
	"vhdl":            "entity foo is\nend entity;\n",
	"wat":             "(module\n  (func $foo))\n",
	"llvm":            "define i32 @foo() {\n  ret i32 0\n}\n",
	"asm":             "main:\n    ret\n",
	"disassembly":     "0x400601 <foo+33>   sub    %r12,%rbp\n",
	"tablegen":        "def Foo;\nclass Bar {}\n",
	"uxntal":          "%foo { #01 }\n",
	"nim":             "proc foo() = discard\ntype Bar = int\n",
	"graphql":         "type Query { hello: String }\n",
	"proto":           "message Foo {\n  string bar = 1;\n}\n",
	"smithy":          "$version: \"2\"\nnamespace example\nstructure Foo {\n}\n",
	"fidl":            "library example;\ntype Foo = struct {};\n",
	"facility":        "service Example {\n}\n",
	"ebnf":            "rule = 'a' | 'b';\n",
	"sparql":          "PREFIX foo: <http://x>\nSELECT * WHERE {}\n",
	"turtle":          "@prefix foo: <http://x> .\n",
	"promql":          "http_requests_total\n",
	"rego":            "package foo\nallow { true }\n",
	"ql":              "class Foo extends int {}\n",
	"sql":             "CREATE TABLE users (id int);\nCREATE VIEW v AS SELECT 1;\n",
	"svelte":          "{#snippet foo()}{/snippet}\n",
	"vue":             "<template><div></div></template>\n",
	"astro":           "<div></div>\n",
	"blade":           "@section('content')\n@endsection\n",
	"heex":            "<div></div>\n",
	"css":             ".foo { color: red; }\n#bar { color: blue; }\n",
	"scss":            "@mixin foo { color: red; }\n.bar { color: blue; }\n",
	"less":            ".foo() { color: red; }\n",
	"ruby":            "class Engine\n  def do_it(a)\n    1\n  end\nend\nmodule M\nend\n",
	"c":               "const unsigned int kNoCellIdx = 1;\nstruct Cell {};\nint run(void) { return 0; }\n",
	"cpp":             "const uint32_t kNoCellIdx = 1;\nclass Cell {};\nint run() { return 0; }\n",
	"go":              "package p\nconst Max = 1\nvar Current = 2\ntype Box struct{}\nfunc Run() {}\nfunc (Box) Size() {}\n",
	"typescript":      "export const Max = 1;\nexport type Point = { x: number };\nexport class Box { size(): number { return 1; } }\nexport function run(): void {}\n",
	"swift":           "class Engine {\n  func run() {}\n}\nstruct Point {}\nenum State {}\nprotocol Runner {}\n",
	"elixir":          "defmodule M do\n  def run(a) do\n    1\n  end\n  defp priv() do\n    2\n  end\nend\n",
	"julia":           "struct Engine\n  n::Int\nend\nfunction run(a)\n  1\nend\nrun2(x) = x\n",
	"r":               "run <- function(a) { 1 }\nf2 = function(b) { 2 }\n",
	"circom":          "template Run() {\n  signal input a;\n}\nfunction f(x) {\n  return x;\n}\n",
	"nix":             "{ foo = 1; bar = 2; }\n",
	"dhall":           "let foo = 1 in foo\n",
	"godot_resource":  "[gd_resource type=\"X\"]\n[resource]\n",
	"cylc":            "[scheduling]\n[[graph]]\n",
	"beancount":       "2020-01-01 open Assets:Cash USD\n",
	"chatito":         "%[greet]\n    hi\n",
	"robot":           "*** Keywords ***\nMy Keyword\n    Log    hi\n",
	"mermaid":         "classDiagram\nclass Foo\n",
	"tmux":            "bind r source-file ~/.tmux.conf\n",
	"todotxt":         "(A) buy milk +shopping\n",
	"enforce":         "class Foo {\n  void Bar() {}\n}\n",
	"eds":             "[section]\nkey = value\n",
	"agda":            "module M where\nfoo : Set\nfoo = ?\n",
	"elsa":            "let foo = \\x -> x\n",
	"regex":           "(?<foo>a)b\n",
	"diff":            "--- a/file.txt\n+++ b/file.txt\n@@ -1 +1 @@\n",
	"comment":         "TODO: do thing\n",
	"gitattributes":   "*.go text\n",
	"dot":             "digraph G { a -> b }\n",
	"ada":             "procedure Hello is\nbegin\n   null;\nend Hello;\n",
	"bass":            "(def foo 1)\n(defn bar [x] x)\n",
	"cooklang":        "Add @flour{2%cups} to bowl.\n",
	"doxygen":         "/**\n * @param x value\n */\n",
	"git_rebase":      "pick a1b2c3 Commit msg\nsquash d4e5f6 Another\n",
	"gitcommit":       "Add feature\n\nBody text here.\n",
	"gitignore":       "*.log\nbuild/\n",
	"ledger":          "2020-01-01 Opening\n    Assets:Cash    $100\n",
	"prolog":          "foo(a, b).\nbar(X) :- foo(X).\n",
	"racket":          "(define (foo x) x)\n(define bar 2)\n",
	"scheme":          "(define (foo x) x)\n(define bar 2)\n",
	"requirements":    "flask==2.0.1\nrequests>=1.0\n",
	"yuck":            "(defwidget foo []\n  (box))\n(defvar bar 1)\n",
}

// Queries need both valid node types and a definition match.
func TestCuratedQueries(t *testing.T) {
	for name, query := range tagsQueries {
		query := strings.TrimSpace(query)
		t.Run(name, func(t *testing.T) {
			if query == "" {
				t.Fatal("empty query")
			}
			sample, ok := querySamples[name]
			if !ok {
				t.Fatal("no sample in querySamples")
			}
			entry := grammars.DetectLanguageByName(name)
			if entry == nil || entry.Language() == nil {
				t.Fatal("no grammar registered")
			}
			lang := entry.Language()

			if _, err := gts.NewQuery(query, lang); err != nil {
				t.Fatalf("query does not compile: %v", err)
			}

			tg, err := gts.NewTagger(lang, query)
			if err != nil {
				t.Fatalf("tagger: %v", err)
			}
			var defs int
			for _, tag := range tg.Tag([]byte(sample)) {
				if strings.HasPrefix(tag.Kind, "definition.") {
					defs++
				}
			}
			if defs == 0 {
				t.Fatal("sample produced no definition tags")
			}
		})
	}
}

// TestNoOrphanSamples keeps samples aligned with queries.
func TestNoOrphanSamples(t *testing.T) {
	for name := range querySamples {
		if _, ok := tagsQueries[name]; !ok {
			t.Errorf("%s: sample has no corresponding query in tagsQueries", name)
		}
	}
}

// Grammars without nameable definitions are exempt from tag queries.
func TestEveryGrammarHasTags(t *testing.T) {
	testutil.SkipIfShort(t, "walks every shipped tree-sitter grammar")
	noDefinitions := map[string]bool{
		"eex":               true, // embedded Elixir, markup shell only
		"embedded_template": true, // ERB/EJS, markup shell only
		"jinja2":            true, // expression grammar, no statement defs
		"pug":               true, // indentation grammar, mixins do not parse standalone
		"wolfram":           true, // only generic prefix/infix/call nodes
	}
	for _, entry := range grammars.AllLanguages() {
		if noDefinitions[entry.Name] {
			continue
		}
		if strings.TrimSpace(grammars.ResolveTagsQuery(entry)) == "" {
			t.Errorf("%s: no tags query (add a curated query in queries.go or add to the noDefinitions exception set)", entry.Name)
		}
	}
}
