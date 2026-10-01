package structrewrite

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func run(t *testing.T, req Request) *Result {
	t.Helper()
	res, err := Run(t.Context(), req)
	if err != nil {
		testutil.FailErr(t, "Run", err)
	}
	return res
}

func TestSearchGoCallSingleMetaVar(t *testing.T) {
	src := []byte("package main\n\nfunc main() {\n\tfmt.Println(\"a\")\n\tfmt.Println(\"b\")\n}\n")
	res := run(t, Request{LangName: "go", Source: src, Pattern: `fmt.Println($A)`})
	if len(res.Matches) != 2 {
		t.Fatalf("want 2 matches, got %d: %+v", len(res.Matches), res.Matches)
	}
	if got := res.Matches[0].Bindings["A"]; got != `"a"` {
		t.Fatalf("first binding A = %q, want %q", got, `"a"`)
	}
	if got := res.Matches[1].Bindings["A"]; got != `"b"` {
		t.Fatalf("second binding A = %q, want %q", got, `"b"`)
	}
}

func TestRewriteGoCall(t *testing.T) {
	src := []byte("package main\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n")
	res := run(t, Request{LangName: "go", Source: src, Pattern: `fmt.Println($A)`, Fix: `log.Info($A)`})
	if !res.Changed {
		t.Fatal("expected Changed=true")
	}
	want := "package main\n\nfunc main() {\n\tlog.Info(\"hi\")\n}\n"
	if string(res.Rewritten) != want {
		t.Fatalf("rewrite mismatch:\n got: %q\nwant: %q", res.Rewritten, want)
	}
}

func TestEllipsisCapturesArgs(t *testing.T) {
	src := []byte("package main\n\nfunc main() {\n\tfoo(1, 2, 3)\n}\n")
	res := run(t, Request{LangName: "go", Source: src, Pattern: `foo($$$ARGS)`, Fix: `bar($$$ARGS)`})
	if len(res.Matches) != 1 {
		t.Fatalf("want 1 match, got %d", len(res.Matches))
	}
	if got := res.Matches[0].Bindings["ARGS"]; got != "1, 2, 3" {
		t.Fatalf("ARGS = %q, want %q", got, "1, 2, 3")
	}
	want := "package main\n\nfunc main() {\n\tbar(1, 2, 3)\n}\n"
	if string(res.Rewritten) != want {
		t.Fatalf("rewrite mismatch:\n got: %q\nwant: %q", res.Rewritten, want)
	}
}

func TestConsistentMetaVarBinding(t *testing.T) {
	// Repeated metavariables bind identical source text.
	src := []byte("package main\n\nfunc f() {\n\t_ = x == x\n\t_ = y == z\n}\n")
	res := run(t, Request{LangName: "go", Source: src, Pattern: `$A == $A`})
	if len(res.Matches) != 1 {
		t.Fatalf("want 1 match (x == x only), got %d: %+v", len(res.Matches), res.Matches)
	}
	if got := res.Matches[0].Bindings["A"]; got != "x" {
		t.Fatalf("A = %q, want x", got)
	}
}

func TestTerminalDoesNotOverMatch(t *testing.T) {
	// Concrete operators constrain the match.
	src := []byte("package main\n\nfunc f() {\n\t_ = a + b\n\t_ = a - b\n}\n")
	res := run(t, Request{LangName: "go", Source: src, Pattern: `$X + $Y`})
	if len(res.Matches) != 1 {
		t.Fatalf("want 1 match (the + only), got %d", len(res.Matches))
	}
}

func TestTypeScriptUsesDollarExpando(t *testing.T) {
	src := []byte("const a = foo(1);\nconst b = foo(2);\n")
	res := run(t, Request{LangName: "typescript", Source: src, Pattern: `foo($A)`, Fix: `bar($A)`})
	if len(res.Matches) != 2 {
		t.Fatalf("want 2 matches, got %d", len(res.Matches))
	}
	want := "const a = bar(1);\nconst b = bar(2);\n"
	if string(res.Rewritten) != want {
		t.Fatalf("rewrite mismatch:\n got: %q\nwant: %q", res.Rewritten, want)
	}
}

func TestNoMatchLeavesSourceUnchanged(t *testing.T) {
	src := []byte("package main\n\nfunc main() {}\n")
	res := run(t, Request{LangName: "go", Source: src, Pattern: `fmt.Println($A)`, Fix: `log.Info($A)`})
	if res.Changed {
		t.Fatal("expected Changed=false")
	}
	if string(res.Rewritten) != string(src) {
		t.Fatalf("source should be unchanged, got %q", res.Rewritten)
	}
}

func TestUnknownGrammar(t *testing.T) {
	_, err := Run(t.Context(), Request{LangName: "not-a-language", Source: []byte("x"), Pattern: "x"})
	if err == nil {
		t.Fatal("expected error for unknown grammar")
	}
	errLanguageUnknown := &ErrLanguageUnknown{}
	if !errors.As(err, &errLanguageUnknown) {
		t.Fatalf("want *ErrLanguageUnknown, got %T", err)
	}
}

func TestFilenameDetection(t *testing.T) {
	src := []byte("package main\n\nfunc main() {\n\tfmt.Println(1)\n}\n")
	res := run(t, Request{Filename: "main.go", Source: src, Pattern: `fmt.Println($A)`})
	if res.Language != "go" {
		t.Fatalf("language = %q, want go", res.Language)
	}
	if len(res.Matches) != 1 {
		t.Fatalf("want 1 match, got %d", len(res.Matches))
	}
}

func TestInvalidPatternHasDistinctFailureType(t *testing.T) {
	_, err := Run(t.Context(), Request{LangName: "go", Source: []byte("package p\n"), Pattern: ""})
	var invalid *PatternError
	if !errors.As(err, &invalid) || !errors.Is(err, errPatternNotFaithful) {
		t.Fatalf("pattern compilation lost its failure kind or cause: %v", err)
	}
	_, err = Run(t.Context(), Request{LangName: "unknown-grammar", Source: []byte("package p\n"), Pattern: ""})
	if errors.As(err, &invalid) {
		t.Fatalf("grammar failure was classified as a pattern failure: %v", err)
	}
}
