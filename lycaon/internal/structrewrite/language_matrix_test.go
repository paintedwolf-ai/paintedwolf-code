package structrewrite

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/filekind"
	"github.com/lycaon/lycaon/internal/repomap"
	"github.com/lycaon/lycaon/internal/testutil"
)

// shapePattern defines expected captures for one language pattern.
type shapePattern struct {
	shape    string // call | func | method | type
	pattern  string
	bindings map[string]string
}

// languageShapes pairs one fixture with its searched shapes.
type languageShapes struct {
	lang     string
	src      string
	patterns []shapePattern
}

// languageShapeMatrix covers each supported language and common search shape.
var languageShapeMatrix = []languageShapes{
	{"go", "package p\n\ntype Engine struct {\n\tn int\n}\n\nfunc run(a int) string {\n\tfmt.Println(a)\n\treturn \"\"\n}\n\nfunc (e *Engine) Do(a int) error { return nil }\n", []shapePattern{
		{"call", `fmt.Println($A)`, map[string]string{"A": "a"}},
		{"func", `func $NAME($$$ARGS) $RET { $$$BODY }`, map[string]string{"NAME": "run"}},
		{"method", `func ($R $T) $NAME($$$ARGS) $RET { $$$BODY }`, map[string]string{"NAME": "Do"}},
		{"type", `type $NAME struct { $$$FIELDS }`, map[string]string{"NAME": "Engine"}},
	}},
	{"python", "class Engine:\n    def do(self, a):\n        print(a)\n\ndef run(a, b):\n    return a\n", []shapePattern{
		{"call", `print($A)`, map[string]string{"A": "a"}},
		{"func", `def $NAME($$$ARGS): $$$BODY`, map[string]string{"NAME": "do"}},
		{"type", `class $NAME: $$$BODY`, map[string]string{"NAME": "Engine"}},
	}},
	{"java", "class Engine {\n    int n;\n    void doIt(int a) { log(a); }\n}\n", []shapePattern{
		{"call", `log($A)`, map[string]string{"A": "a"}},
		{"method", `void $NAME($$$ARGS) { $$$BODY }`, map[string]string{"NAME": "doIt"}},
		{"type", `class $NAME { $$$BODY }`, map[string]string{"NAME": "Engine"}},
	}},
	{"javascript", "class Engine {\n  do(a) { log(a); }\n}\nfunction run(a, b) { return a; }\n", []shapePattern{
		{"call", `log($A)`, map[string]string{"A": "a"}},
		{"func", `function $NAME($$$ARGS) { $$$BODY }`, map[string]string{"NAME": "run"}},
		{"method", `class $C { $NAME($$$ARGS) { $$$BODY } }`, nil},
		{"type", `class $NAME { $$$BODY }`, map[string]string{"NAME": "Engine"}},
	}},
	{"typescript", "class Engine {\n  do(a: number) { log(a); }\n}\nfunction run(a: number) { return a; }\n", []shapePattern{
		{"call", `log($A)`, map[string]string{"A": "a"}},
		{"func", `function $NAME($$$ARGS) { $$$BODY }`, map[string]string{"NAME": "run"}},
		{"type", `class $NAME { $$$BODY }`, map[string]string{"NAME": "Engine"}},
	}},
	{"c_sharp", "class Engine {\n    int n;\n    void DoIt(int a) { Log(a); }\n}\n", []shapePattern{
		{"call", `Log($A)`, map[string]string{"A": "a"}},
		{"method", `class $C { $$$X void $NAME($$$ARGS) { $$$BODY } $$$Y }`, map[string]string{"NAME": "DoIt"}},
		{"type", `class $NAME { $$$BODY }`, map[string]string{"NAME": "Engine"}},
	}},
	{"scala", "object O {\n  def run(a: Int) = { log(a) }\n}\n", []shapePattern{
		{"call", `log($A)`, map[string]string{"A": "a"}},
		{"func", `def $NAME($$$ARGS) = { $$$BODY }`, map[string]string{"NAME": "run"}},
	}},
	{"rust", "struct Engine { n: i32 }\nimpl Engine {\n    fn do_it(&self, a: i32) { log(a); }\n}\nfn run(a: i32) { log(a); }\n", []shapePattern{
		{"call", `log($A)`, map[string]string{"A": "a"}},
		{"func", `fn $NAME($$$ARGS) { $$$BODY }`, nil},
		{"type", `struct $NAME { $$$F }`, map[string]string{"NAME": "Engine"}},
		{"method", `impl $T { $$$BODY }`, map[string]string{"T": "Engine"}},
	}},
	{"ruby", "class Engine\n  def do_it(a)\n    log(a)\n  end\nend\n", []shapePattern{
		{"call", `log($A)`, map[string]string{"A": "a"}},
		{"func", `def $NAME($$$ARGS) $$$BODY end`, map[string]string{"NAME": "do_it"}},
		{"type", "class $NAME\n$$$BODY\nend", map[string]string{"NAME": "Engine"}},
	}},
	{"c", "struct Engine { int n; };\nvoid run(int a) { log(a); }\n", []shapePattern{
		{"call", `log($A)`, map[string]string{"A": "a"}},
		{"func", `void $NAME($$$ARGS) { $$$BODY }`, map[string]string{"NAME": "run"}},
		{"type", `struct $NAME { $$$F }`, map[string]string{"NAME": "Engine"}},
	}},
	{"cpp", "class Engine { int n; };\nint run(int a) { log(a); return a; }\n", []shapePattern{
		{"call", `log($A)`, map[string]string{"A": "a"}},
		{"func", `int $NAME($$$ARGS) { $$$BODY }`, map[string]string{"NAME": "run"}},
		{"type", `class $NAME { $$$F }`, map[string]string{"NAME": "Engine"}},
	}},
	{"php", "<?php\nclass Engine {\n  function doIt($a) { log($a); }\n}\nfunction run($a) { log($a); }\n", []shapePattern{
		{"call", `log($A)`, map[string]string{"A": "$a"}},
		{"func", `function $NAME($$$ARGS) { $$$BODY }`, nil},
		{"type", `class $NAME { $$$BODY }`, map[string]string{"NAME": "Engine"}},
	}},
	{"kotlin", "class Engine {\n    fun doIt(a: Int) { log(a) }\n}\nfun run(a: Int) { log(a) }\n", []shapePattern{
		{"call", `log($A)`, map[string]string{"A": "a"}},
		{"func", `fun $NAME($$$ARGS) { $$$BODY }`, nil},
		{"type", `class $NAME { $$$BODY }`, map[string]string{"NAME": "Engine"}},
	}},
	{"swift", "class Engine {\n    func doIt(a: Int) { log(a) }\n}\nfunc run(a: Int) { log(a) }\n", []shapePattern{
		{"call", `log($A)`, map[string]string{"A": "a"}},
		{"func", `func $NAME($$$ARGS) { $$$BODY }`, nil},
		{"type", `class $NAME { $$$BODY }`, map[string]string{"NAME": "Engine"}},
	}},
	{"dart", "class Engine {\n  void doIt(int a) { log(a); }\n}\nvoid run(int a) { log(a); }\n", []shapePattern{
		{"call", `log($A);`, map[string]string{"A": "a"}},
		{"method", `class $C { void $NAME($$$ARGS) { $$$BODY } }`, nil},
		{"type", `class $NAME { $$$BODY }`, map[string]string{"NAME": "Engine"}},
	}},
	{"lua", "function run(a, b)\n  log(a)\nend\n", []shapePattern{
		{"call", `log($A)`, map[string]string{"A": "a"}},
		{"func", `function $NAME($$$ARGS) $$$BODY end`, map[string]string{"NAME": "run"}},
	}},
	{"elixir", "defmodule M do\n  def run(a) do\n    log(a)\n  end\nend\n", []shapePattern{
		{"call", `log($A)`, map[string]string{"A": "a"}},
		{"func", `def $NAME($$$ARGS) do $$$BODY end`, map[string]string{"NAME": "run"}},
		{"type", `defmodule $NAME do $$$BODY end`, map[string]string{"NAME": "M"}},
	}},
	{"julia", "struct Engine\n    n::Int\nend\nfunction run(a)\n    log(a)\nend\n", []shapePattern{
		{"call", `log($A)`, map[string]string{"A": "a"}},
		{"func", `function $NAME($$$ARGS) $$$BODY end`, map[string]string{"NAME": "run"}},
		{"type", `struct $NAME $$$F end`, map[string]string{"NAME": "Engine"}},
	}},
	{"r", "run <- function(a, b) { log(a) }\n", []shapePattern{
		{"call", `log($A)`, map[string]string{"A": "a"}},
		{"func", `$NAME <- function($$$ARGS) { $$$BODY }`, map[string]string{"NAME": "run"}},
	}},
	{"clojure", "(defn run [a b] (log a))\n", []shapePattern{
		{"call", `(log $A)`, map[string]string{"A": "a"}},
		{"func", `(defn $NAME [$$$ARGS] $$$BODY)`, map[string]string{"NAME": "run"}},
	}},
	{"bash", "run() {\n  log hi\n}\n", []shapePattern{
		{"func", `$NAME() { $$$BODY }`, map[string]string{"NAME": "run"}},
		{"call", `log $A`, map[string]string{"A": "hi"}},
	}},
	{"perl", "package Engine;\nsub run {\n  my ($a) = @_;\n  log($a);\n}\n", []shapePattern{
		{"call", `log($A)`, map[string]string{"A": "$a"}},
		{"func", `sub $NAME { $$$BODY }`, map[string]string{"NAME": "run"}},
	}},
	{"powershell", "function Invoke-Run {\n  param($Value)\n  Write-Output $Value\n}\n", []shapePattern{
		{"call", `Write-Output $A`, map[string]string{"A": " $Value"}},
		{"func", `function $NAME { $$$BODY }`, map[string]string{"NAME": "Invoke-Run"}},
	}},
	{"groovy", "class Engine {\n  def run(value) { log(value) }\n}\n", []shapePattern{
		{"call", `log($A)`, map[string]string{"A": "value"}},
		{"func", `def $NAME($$$ARGS) { $$$BODY }`, map[string]string{"NAME": "run"}},
		{"type", `class $NAME { $$$BODY }`, map[string]string{"NAME": "Engine"}},
	}},
	{"solidity", "contract C {\n  function run(uint a) public { log(a); }\n}\n", []shapePattern{
		{"call", `log($A)`, map[string]string{"A": "a"}},
		{"func", `function $NAME($$$ARGS) public { $$$BODY }`, map[string]string{"NAME": "run"}},
		{"type", `contract $NAME { $$$BODY }`, map[string]string{"NAME": "C"}},
	}},
	{"hcl", "resource \"aws_s3_bucket\" \"b\" {\n  acl = \"private\"\n}\n", []shapePattern{
		{"type", `resource $TYPE $NAME { $$$BODY }`, map[string]string{"NAME": `"b"`}},
		{"call", `acl = $V`, map[string]string{"V": `"private"`}},
	}},
	{"dockerfile", "FROM alpine\nRUN apk add curl\n", []shapePattern{
		{"call", `RUN $CMD`, map[string]string{"CMD": "apk add curl"}},
		{"type", `FROM $IMG`, nil},
	}},
	// Remaining supported languages.
	{"apex", "public class Engine {\n    public void doIt(Integer a) { log(a); }\n}\n", []shapePattern{
		{"call", `log($A)`, map[string]string{"A": "a"}},
		{"type", `public class $NAME { $$$BODY }`, map[string]string{"NAME": "Engine"}},
	}},
	{"hack", "<?hh\nclass Engine {\n  public function doIt(int $a): void { log($a); }\n}\n", []shapePattern{
		{"call", `log($A)`, nil},
		{"type", `class $NAME { $$$BODY }`, map[string]string{"NAME": "Engine"}},
	}},
	{"ocaml", "type engine = { n : int }\nlet run a = log a\n", []shapePattern{
		{"func", `let $NAME $$$ARGS = $$$BODY`, nil},
		{"call", `log $A`, nil},
	}},
	{"scheme", "(define (run a) (log a))\n", []shapePattern{
		{"call", `(log $A)`, map[string]string{"A": "a"}},
		{"func", `(define ($NAME $$$ARGS) $$$BODY)`, nil},
	}},
	{"commonlisp", "(defun run (a) (log a))\n", []shapePattern{
		{"call", `(log $A)`, map[string]string{"A": "a"}},
		{"func", `(defun $NAME ($$$ARGS) $$$BODY)`, nil},
	}},
	{"cairo", "fn run(a: felt252) { log(a); }\n", []shapePattern{
		{"call", `log($A)`, map[string]string{"A": "a"}},
		{"func", `fn $NAME($$$ARGS) { $$$BODY }`, nil},
	}},
	{"circom", "template Run() {\n  signal input a;\n}\n", []shapePattern{
		{"type", `template $NAME() { $$$BODY }`, nil},
	}},
	{"move", "module 0x1::engine {\n    public fun run(a: u64) { log(a); }\n}\n", []shapePattern{
		{"call", `log($A)`, map[string]string{"A": "a"}},
		{"func", `public fun $NAME($$$ARGS) { $$$BODY }`, nil},
	}},
	{"ql", "class Engine extends Expr {\n  Engine() { this = 1 }\n}\n", []shapePattern{
		{"type", `class $NAME extends $BASE { $$$BODY }`, nil},
	}},
	{"promql", "sum(rate(http_requests_total[5m]))\n", []shapePattern{
		{"call", `sum($A)`, nil},
	}},
	{"proto", "syntax = \"proto3\";\nmessage Engine {\n  int32 n = 1;\n}\n", []shapePattern{
		{"type", `message $NAME { $$$BODY }`, map[string]string{"NAME": "Engine"}},
	}},
	{"jsonnet", "local run(a) = a + 1;\n{ x: run(1) }\n", []shapePattern{
		{"func", `local $NAME($$$ARGS) = $$$BODY; $$$REST`, nil},
	}},
	{"json", "{\"name\": \"lycaon\", \"n\": 1}\n", []shapePattern{
		{"call", `{"name": "$V", $$$REST}`, nil},
	}},
	{"yaml", "name: lycaon\nn: 1\n", []shapePattern{
		{"call", `name: $V`, nil},
	}},
	{"xml", "<root><item id=\"1\">hi</item></root>\n", []shapePattern{
		{"type", `<item id="$ID">$$$BODY</item>`, nil},
	}},
	{"html", "<h1>Title</h1>\n<div class=\"a\"><span>hi</span></div>\n", []shapePattern{
		{"type", `<span>$$$BODY</span>`, nil},
	}},
	{"vue", "<template><div>{{ msg }}</div></template>\n", []shapePattern{
		{"type", `<div>$$$BODY</div>`, nil},
	}},
}

func TestLanguageShapeMatrix(t *testing.T) {
	for _, lang := range languageShapeMatrix {
		for _, p := range lang.patterns {
			t.Run(lang.lang+"/"+p.shape, func(t *testing.T) {
				res := run(t, Request{LangName: lang.lang, Source: []byte(lang.src), Pattern: p.pattern})
				if len(res.Matches) == 0 {
					t.Fatalf("pattern %q found nothing in:\n%s", p.pattern, lang.src)
				}
				for k, want := range p.bindings {
					if got := res.Matches[0].Bindings[k]; got != want {
						t.Fatalf("pattern %q binding $%s = %q, want %q (all: %+v)",
							p.pattern, k, got, want, res.Matches[0].Bindings)
					}
				}
			})
		}
	}
}

// supportedLanguages shares filekind's shipping language set.
var supportedLanguages = filekind.SupportedLanguages()

func TestEverySupportedLanguageHasShapeCoverage(t *testing.T) {
	covered := map[string]bool{}
	for _, lang := range languageShapeMatrix {
		covered[lang.lang] = true
	}
	var missing []string
	for _, l := range supportedLanguages {
		if !covered[l] {
			missing = append(missing, l)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("supported languages with no shape coverage: %v", missing)
	}
	for l := range covered {
		if !contains(supportedLanguages, l) {
			t.Errorf("matrix covers %q, which docs/supported-languages.md does not list", l)
		}
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// Each supported language contributes definitions from its fixture.
func TestEverySupportedLanguageHasSymbolOutline(t *testing.T) {
	for _, lang := range languageShapeMatrix {
		t.Run(lang.lang, func(t *testing.T) {
			spans, resolved, ok, err := repomap.DefinitionSpans(t.Context(), lang.lang, "", []byte(lang.src))
			testutil.FailErr(t, "analyze definitions", err)
			if !ok {
				t.Fatalf("no grammar resolved for %q", lang.lang)
			}
			if len(spans) == 0 {
				t.Fatalf("%s (grammar %q) produced no definition spans — read(symbol=…) "+
					"and repo_map are all empty for this language. Add a "+
					"curated tags query in internal/repomap/gtsqueries/queries.go.",
					lang.lang, resolved)
			}
			for _, s := range spans {
				if strings.TrimSpace(s.Name) == "" {
					t.Fatalf("%s produced a definition span with no name: %+v", lang.lang, s)
				}
			}
		})
	}
}

// Placeholder captures retain their source text in each grammar.
func TestExpandoSurvivesEverySupportedLanguage(t *testing.T) {
	for _, lang := range languageShapeMatrix {
		t.Run(lang.lang, func(t *testing.T) {
			ts, tsName, err := resolveLanguage(lang.lang, "")
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			for _, p := range lang.patterns {
				if !strings.Contains(p.pattern, "$") {
					continue
				}
				cp, err := compilePattern(context.Background(), ts, tsName, p.pattern)
				if err != nil {
					t.Fatalf("compile %q: %v", p.pattern, err)
				}
				if !hasMetaNode(cp.root) {
					t.Fatalf("%q compiled with no metavariable — expando %q is not an "+
						"identifier character for %s", p.pattern, string(cp.expando), tsName)
				}
			}
		})
	}
}

func hasMetaNode(n *patternNode) bool {
	if n == nil {
		return false
	}
	if n.meta != nil {
		return true
	}
	for _, c := range n.children {
		if hasMetaNode(c) {
			return true
		}
	}
	return false
}
