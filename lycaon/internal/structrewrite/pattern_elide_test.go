package structrewrite

import (
	"strings"
	"testing"
)

// Declaration slots can require types or punctuation around sequence variables.
func TestElidedDeclarationPatternsAcrossGrammars(t *testing.T) {
	cases := []struct {
		name     string
		lang     string
		pattern  string
		src      string
		want     int
		bindings map[string]string // checked on the first match
	}{
		{
			name:    "java method",
			lang:    "java",
			pattern: `void $NAME($$$ARGS) { $$$BODY }`,
			src:     "class C { void run(int a, String b) { log(a); } }",
			want:    1,
			bindings: map[string]string{
				"NAME": "run", "ARGS": "int a, String b", "BODY": "log(a);",
			},
		},
		{
			name:     "java class body",
			lang:     "java",
			pattern:  `class $NAME { $$$BODY }`,
			src:      "class Foo { int a; }",
			want:     1,
			bindings: map[string]string{"NAME": "Foo", "BODY": "int a;"},
		},
		{
			name:     "c function",
			lang:     "c",
			pattern:  `void $NAME($$$ARGS) { $$$BODY }`,
			src:      "void run(int a) { f(a); }\n",
			want:     1,
			bindings: map[string]string{"NAME": "run", "ARGS": "int a", "BODY": "f(a);"},
		},
		{
			name:     "c struct",
			lang:     "c",
			pattern:  `struct $NAME { $$$F };`,
			src:      "struct S { int a; char b; };\n",
			want:     1,
			bindings: map[string]string{"NAME": "S", "F": "int a; char b;"},
		},
		{
			name:     "cpp function",
			lang:     "cpp",
			pattern:  `int $NAME($$$ARGS) { $$$BODY }`,
			src:      "int run(int a) { return a; }\n",
			want:     1,
			bindings: map[string]string{"NAME": "run", "ARGS": "int a", "BODY": "return a;"},
		},
		{
			name:     "rust struct",
			lang:     "rust",
			pattern:  `struct $NAME { $$$F }`,
			src:      "struct S { a: i32, b: u8 }\n",
			want:     1,
			bindings: map[string]string{"NAME": "S", "F": "a: i32, b: u8"},
		},
		{
			name:     "rust impl",
			lang:     "rust",
			pattern:  `impl $T { $$$BODY }`,
			src:      "impl Foo { fn a(&self) {} }\n",
			want:     1,
			bindings: map[string]string{"T": "Foo", "BODY": "fn a(&self) {}"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := run(t, Request{LangName: tc.lang, Source: []byte(tc.src), Pattern: tc.pattern})
			if len(res.Matches) != tc.want {
				t.Fatalf("want %d matches, got %d: %+v", tc.want, len(res.Matches), res.Matches)
			}
			for k, want := range tc.bindings {
				if got := res.Matches[0].Bindings[k]; got != want {
					t.Fatalf("binding %s = %q, want %q", k, got, want)
				}
			}
		})
	}
}

// An elided sequence can capture zero nodes.
func TestElidedEllipsisMatchesEmptySequence(t *testing.T) {
	res := run(t, Request{LangName: "java", Pattern: `void $NAME($$$ARGS) { $$$BODY }`,
		Source: []byte("class C { void run(int a) { log(a); } void empty() {} }")})
	if len(res.Matches) != 2 {
		t.Fatalf("want 2 matches, got %d: %+v", len(res.Matches), res.Matches)
	}
	if got := res.Matches[1].Bindings["ARGS"]; got != "" {
		t.Fatalf("empty parameter list bound ARGS=%q, want empty", got)
	}
}

// Elision preserves the declaration kind.
func TestElidedPatternStaysWithinItsNodeKind(t *testing.T) {
	res := run(t, Request{LangName: "java", Pattern: `void $NAME($$$ARGS) { $$$BODY }`,
		Source: []byte("class C { int field; C() {} void m() { x(); } }")})
	if len(res.Matches) != 1 {
		t.Fatalf("want only the method, got %d: %+v", len(res.Matches), res.Matches)
	}
	if !strings.HasPrefix(res.Matches[0].Text, "void m()") {
		t.Fatalf("matched %q, want the void method", res.Matches[0].Text)
	}

	rs := run(t, Request{LangName: "rust", Pattern: `struct $NAME { $$$F }`,
		Source: []byte("struct A { x: i32 }\nstruct B;\nstruct C();\n")})
	if len(rs.Matches) != 1 {
		t.Fatalf("want only the field struct, got %d: %+v", len(rs.Matches), rs.Matches)
	}
}

// A body distinguishes a method definition from an interface declaration.
func TestElidedBodyPatternRequiresABody(t *testing.T) {
	res := run(t, Request{LangName: "java", Pattern: `void $N($$$A) { $$$B }`,
		Source: []byte("interface I { void m(); }")})
	if len(res.Matches) != 0 {
		t.Fatalf("want 0 matches for a bodiless method, got %+v", res.Matches)
	}
}

// Nested sequences bind to separate child lists.
func TestElidedNestedEllipses(t *testing.T) {
	res := run(t, Request{LangName: "c", Pattern: `if ($C) { $$$A } else { $$$B }`,
		Source: []byte("void f() { if (x) { a(); } else { b(); } }")})
	if len(res.Matches) != 1 {
		t.Fatalf("want 1 match, got %d: %+v", len(res.Matches), res.Matches)
	}
	m := res.Matches[0]
	if m.Bindings["A"] != "a();" || m.Bindings["B"] != "b();" {
		t.Fatalf("branch bindings crossed over: %+v", m.Bindings)
	}
}

// Captured source retains its separators during substitution.
func TestElidedPatternRewrites(t *testing.T) {
	res := run(t, Request{LangName: "java",
		Pattern: `void $NAME($$$ARGS) { $$$BODY }`,
		Fix:     `void $NAME($$$ARGS) { log(); $$$BODY }`,
		Source:  []byte("class C { void m(int a) { x(); } }")})
	if !res.Changed {
		t.Fatal("expected the rewrite to change the source")
	}
	want := "class C { void m(int a) { log(); x(); } }"
	if string(res.Rewritten) != want {
		t.Fatalf("rewrite mismatch:\n got: %q\nwant: %q", res.Rewritten, want)
	}
}

// Directly parsed patterns retain their literal separators.
func TestElisionDoesNotPreemptADirectParse(t *testing.T) {
	res := run(t, Request{LangName: "go", Pattern: `foo($$$A, $B)`,
		Source: []byte("package p\nfunc m() { foo(1, 2, 3) }\n")})
	if len(res.Matches) != 1 {
		t.Fatalf("want 1 match, got %d: %+v", len(res.Matches), res.Matches)
	}
	if got := res.Matches[0].Bindings["B"]; got != "3" {
		t.Fatalf("B = %q, want %q", got, "3")
	}
	// Literal separators constrain the target.
	none := run(t, Request{LangName: "go", Pattern: `foo($$$A, $B)`,
		Source: []byte("package p\nfunc m() { foo(1) }\n")})
	if len(none.Matches) != 0 {
		t.Fatalf("want 0 matches without a separator, got %+v", none.Matches)
	}
}

func TestStripEllipsesDropsOrphanedSeparators(t *testing.T) {
	cases := []struct {
		in, want string
		holes    int
	}{
		{"foo(µµµA)", "foo()", 1},
		{"foo(µµµA, µB)", "foo(µB)", 1},
		{"foo(µA, µµµB)", "foo(µA)", 1},
		{"foo(µµµA, µµµB)", "foo()", 2},
		// Whitespace around a removed placeholder stays; parsing ignores it.
		{"void µN(µµµARGS) { µµµBODY }", "void µN() {  }", 2},
		{"foo(µA)", "foo(µA)", 0},
	}
	for _, tc := range cases {
		got, holes := stripEllipses(tc.in, 'µ')
		if got != tc.want || len(holes) != tc.holes {
			t.Fatalf("stripEllipses(%q) = %q/%d holes, want %q/%d", tc.in, got, len(holes), tc.want, tc.holes)
		}
	}
}
