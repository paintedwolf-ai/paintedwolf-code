package structrewrite

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// declSrc contains distinct declaration and closure shapes.
const declSrc = `package summarize

func Run(a int) string { return "x" }

func (e *Engine) Run(ctx int) error { return nil }

func helper(t *T) {
	f := func(t *T) { println("closure") }
	_ = f
}

type Engine struct{}
`

func TestGoFuncDeclarationMatchesByName(t *testing.T) {
	res := run(t, Request{LangName: "go", Source: []byte(declSrc),
		Pattern: `func Run($$$ARGS) $RET { $$$BODY }`})
	if len(res.Matches) != 1 {
		t.Fatalf("want 1 match, got %d: %+v", len(res.Matches), res.Matches)
	}
	m := res.Matches[0]
	if !strings.HasPrefix(m.Text, "func Run(a int) string") {
		t.Fatalf("matched the wrong node: %q", m.Text)
	}
	if got := m.Bindings["RET"]; got != "string" {
		t.Fatalf("RET = %q, want %q", got, "string")
	}
	if got := m.Bindings["ARGS"]; got != "a int" {
		t.Fatalf("ARGS = %q, want %q", got, "a int")
	}
}

// Named functions cannot match anonymous closures.
func TestGoFuncPatternNameIsNotDropped(t *testing.T) {
	named := run(t, Request{LangName: "go", Source: []byte(declSrc),
		Pattern: `func Run($$$ARGS) $RET { $$$BODY }`})
	other := run(t, Request{LangName: "go", Source: []byte(declSrc),
		Pattern: `func helper($$$ARGS) { $$$BODY }`})
	if len(named.Matches) != 1 || len(other.Matches) != 1 {
		t.Fatalf("want 1 match each, got %d and %d", len(named.Matches), len(other.Matches))
	}
	if named.Matches[0].Text == other.Matches[0].Text {
		t.Fatal("patterns with different names returned the same match — the name was dropped")
	}
	for _, m := range named.Matches {
		if strings.Contains(m.Text, "closure") {
			t.Fatalf("named pattern matched an anonymous closure: %q", m.Text)
		}
	}
}

func TestGoMetaVarNameBinds(t *testing.T) {
	res := run(t, Request{LangName: "go", Source: []byte(declSrc),
		Pattern: `func $NAME($$$ARGS) $RET { $$$BODY }`})
	if len(res.Matches) != 1 {
		t.Fatalf("want 1 match, got %d: %+v", len(res.Matches), res.Matches)
	}
	if got := res.Matches[0].Bindings["NAME"]; got != "Run" {
		t.Fatalf("NAME = %q, want %q", got, "Run")
	}
}

func TestGoMethodDeclarationPattern(t *testing.T) {
	res := run(t, Request{LangName: "go", Source: []byte(declSrc),
		Pattern: `func (e *Engine) $NAME($$$ARGS) $RET { $$$BODY }`})
	if len(res.Matches) != 1 {
		t.Fatalf("want 1 match, got %d: %+v", len(res.Matches), res.Matches)
	}
	m := res.Matches[0]
	if got := m.Bindings["NAME"]; got != "Run" {
		t.Fatalf("NAME = %q, want %q", got, "Run")
	}
	if got := m.Bindings["RET"]; got != "error" {
		t.Fatalf("RET = %q, want %q", got, "error")
	}
}

// Grammar fields constrain metavariable slots.
func TestMetaVarDoesNotCrossGrammarSlots(t *testing.T) {
	res := run(t, Request{LangName: "go", Source: []byte(declSrc),
		Pattern: `func $NAME($$$ARGS) $RET`})
	for _, m := range res.Matches {
		if strings.HasPrefix(m.Bindings["RET"], "{") {
			t.Fatalf("RET bound a body block instead of a result type: %q", m.Bindings["RET"])
		}
	}
}

func TestResultLessFuncPatternDoesNotMatchClosure(t *testing.T) {
	res := run(t, Request{LangName: "go", Source: []byte(declSrc),
		Pattern: `func Run($$$ARGS) { $$$BODY }`})
	if len(res.Matches) != 0 {
		t.Fatalf("want 0 matches, got %d: %+v", len(res.Matches), res.Matches)
	}
}

func TestResultLessFuncPatternDoesNotRewriteClosure(t *testing.T) {
	res := run(t, Request{LangName: "go", Source: []byte(declSrc),
		Pattern: `func Run($$$ARGS) { $$$BODY }`, Fix: `func Run($$$ARGS) { panic("x") }`})
	if res.Changed {
		t.Fatalf("pattern rewrote an unrelated node:\n%s", res.Rewritten)
	}
}

func TestUnbalancedPatternIsRejected(t *testing.T) {
	if _, err := Run(t.Context(), Request{LangName: "go", Source: []byte(declSrc),
		Pattern: `func Run($$$ARGS) {`}); err == nil {
		t.Fatal("want an error for an unbalanced fragment")
	}
}

func TestPatternFidelityRejectsDroppedFunctionName(t *testing.T) {
	lang, langName, err := resolveLanguage("go", "")
	testutil.FailErr(t, "resolve Go language", err)
	candidate, err := parsePattern(context.Background(), lang, "func() { }", patternWrappers[langName])
	if err != nil || candidate == nil {
		t.Fatal("valid function literal did not parse")
	}
	defer candidate.tree.Release()
	root := convertNode(candidate.node, lang, candidate.source, expandoChar(langName), nil)
	if patternFaithful(root, "func Run() { }") {
		t.Fatal("pattern accepted a tree missing the function name")
	}
	compiled, err := compilePattern(context.Background(), lang, langName, "func Run() { }")
	testutil.FailErr(t, "compile named function", err)
	if !patternFaithful(compiled.root, "func Run() { }") {
		t.Fatal("compiled declaration lost the function name")
	}
}

func TestPHPFunctionDeclarationPattern(t *testing.T) {
	src := []byte("<?php\nfunction run($a, $b) { echo $a; }\n")
	res := run(t, Request{LangName: "php", Source: src,
		Pattern: `function $NAME($$$ARGS) { $$$BODY }`})
	if len(res.Matches) != 1 {
		t.Fatalf("want 1 match, got %d: %+v", len(res.Matches), res.Matches)
	}
	m := res.Matches[0]
	if m.Bindings["NAME"] != "run" || m.Bindings["ARGS"] != "$a, $b" {
		t.Fatalf("bindings = %+v", m.Bindings)
	}
	other := run(t, Request{LangName: "php", Source: src,
		Pattern: `function nope($$$ARGS) { $$$BODY }`})
	if len(other.Matches) != 0 {
		t.Fatalf("literal name should not match, got %+v", other.Matches)
	}
}

func TestCSharpLocalFunctionPattern(t *testing.T) {
	src := []byte("class C { void M() { void Run(int a) { return; } Run(1); } }")
	res := run(t, Request{LangName: "c_sharp", Source: src,
		Pattern: `void $NAME($$$ARGS) { $$$BODY }`})
	if len(res.Matches) != 1 {
		t.Fatalf("want 1 match, got %d: %+v", len(res.Matches), res.Matches)
	}
	if got := res.Matches[0].Bindings["NAME"]; got != "Run" {
		t.Fatalf("NAME = %q, want %q", got, "Run")
	}
}

func TestGoTypeAndVarDeclarationPatterns(t *testing.T) {
	res := run(t, Request{LangName: "go", Source: []byte(declSrc),
		Pattern: `type $NAME struct { $$$FIELDS }`})
	if len(res.Matches) != 1 {
		t.Fatalf("want 1 type match, got %d: %+v", len(res.Matches), res.Matches)
	}
	if got := res.Matches[0].Bindings["NAME"]; got != "Engine" {
		t.Fatalf("NAME = %q, want %q", got, "Engine")
	}
}

// Expression patterns resolve inside statement context.
func TestExpressionPatternStillUsesWrapper(t *testing.T) {
	src := []byte("package main\n\nfunc main() {\n\te.Run(1, 2)\n}\n")
	res := run(t, Request{LangName: "go", Source: src, Pattern: `$X.Run($$$ARGS)`})
	if len(res.Matches) != 1 {
		t.Fatalf("want 1 match, got %d: %+v", len(res.Matches), res.Matches)
	}
	if got := res.Matches[0].Bindings["X"]; got != "e" {
		t.Fatalf("X = %q, want %q", got, "e")
	}
}
