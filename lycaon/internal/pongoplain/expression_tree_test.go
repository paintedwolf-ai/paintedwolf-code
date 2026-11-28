package pongoplain

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestParseExpressionPrintsEveryKindAsEquivalentSource(t *testing.T) {
	for _, tc := range []struct {
		src, want string
		kind      ExprKind
		op        string
	}{
		{`a or b`, `a or b`, ExprOr, "or"},
		{`a || b`, `a or b`, ExprOr, "or"},
		{`a and b`, `a and b`, ExprAnd, "and"},
		{`a && b`, `a and b`, ExprAnd, "and"},
		{`not a`, `not a`, ExprNot, "not"},
		{`!a`, `not a`, ExprNot, "not"},
		{`a == 1`, `a == 1`, ExprCompare, "=="},
		{`a != 'x'`, `a != "x"`, ExprCompare, "!="},
		{`a <> 1`, `a != 1`, ExprCompare, "!="},
		{`a <= 2`, `a <= 2`, ExprCompare, "<="},
		{`a >= 2`, `a >= 2`, ExprCompare, ">="},
		{`a < 2`, `a < 2`, ExprCompare, "<"},
		{`a > 2`, `a > 2`, ExprCompare, ">"},
		{`a in b`, `a in b`, ExprCompare, "in"},
		{`a + 1`, `a + 1`, ExprArith, "+"},
		{`a - 1`, `a - 1`, ExprArith, "-"},
		{`a * 2`, `a * 2`, ExprArith, "*"},
		{`a / 2`, `a / 2`, ExprArith, "/"},
		{`a % 2`, `a % 2`, ExprArith, "%"},
		{`a ^ 2`, `a ^ 2`, ExprArith, "^"},
		{`a == -1.5`, `a == -1.5`, ExprCompare, "=="},
		{`-3`, `-3`, ExprNeg, "-"},
		{`-not a`, `-not a`, ExprNeg, "-"},
		{`+a`, `a`, ExprIdent, ""},
		{`x|length`, `x|length`, ExprFilter, ""},
		{`x|default:"y"`, `x|default:"y"`, ExprFilter, ""},
		{`x|default:y . z|upper`, `x|default:y.z|upper`, ExprFilter, ""},
		{`x|default:1 . 5`, `x|default:1.5`, ExprFilter, ""},
		{`not (a and b)`, `not (a and b)`, ExprNot, "not"},
		{`(a or b) and c`, `(a or b) and c`, ExprAnd, "and"},
		{`a and b or c`, `a and b or c`, ExprAnd, "and"},
		{`a and (b or c)`, `a and b or c`, ExprAnd, "and"},
		{`(a and b) or c`, `(a and b) or c`, ExprOr, "or"},
		{`not a == b`, `not a == b`, ExprCompare, "=="},
		{`a == b == c`, `a == b == c`, ExprCompare, "=="},
		{`(a == b) == c`, `(a == b) == c`, ExprCompare, "=="},
		{`a ^ b ^ c`, `a ^ b ^ c`, ExprArith, "^"},
		{`(a ^ b) ^ c`, `(a ^ b) ^ c`, ExprArith, "^"},
		{`a - b - c`, `a - b - c`, ExprArith, "-"},
		{`a - (b - c)`, `a - (b - c)`, ExprArith, "-"},
		{`a + (not b)`, `a + (not b)`, ExprArith, "+"},
		{`a . b`, `a.b`, ExprIdent, ""},
		{`a.0`, `a.0`, ExprIdent, ""},
		{`1abc`, `1abc`, ExprIdent, ""},
		{`"a\"b"`, `"a\"b"`, ExprLiteral, ""},
		{`'a\\b'`, `"a\\b"`, ExprLiteral, ""},
		{`True`, `True`, ExprIdent, ""},
		{`None`, `None`, ExprIdent, ""},
		{`nil`, `nil`, ExprIdent, ""},
	} {
		expr, err := ParseExpression(tc.src)
		if err != nil {
			t.Errorf("ParseExpression(%q): %v", tc.src, err)
			continue
		}
		if expr.Kind != tc.kind || expr.Op != tc.op {
			t.Errorf("ParseExpression(%q) = %s %q, want %s %q", tc.src, expr.Kind, expr.Op, tc.kind, tc.op)
		}
		if got := expr.Source(); got != tc.want {
			t.Errorf("Source(%q) = %q, want %q", tc.src, got, tc.want)
		}
		reparsed, err := ParseExpression(expr.Source())
		if err != nil || reparsed.Source() != tc.want {
			t.Errorf("Source(%q) does not reparse to itself: %v", tc.src, err)
		}
	}
}

func TestParseExpressionFailsClosed(t *testing.T) {
	for _, src := range []string{
		"", "   ", `"abc`, `"abc\`, `a @ b`, `a b`, `a or`, `a and`, `not`, `a ==`, `a in`,
		`a +`, `a|`, `a|b.c`, `a|f:`, `(a`, `(`, `)`, `a.`, `in`, `as`, "a\nb", `99999999999999999999`,
		`a[0]`, `f(a)`, `a.b(c)`,
	} {
		if expr, err := ParseExpression(src); err == nil {
			t.Errorf("ParseExpression(%q) = %+v, want an error", src, expr)
		}
	}
}

func TestExprBooleanSkeletonDecidesFromAtoms(t *testing.T) {
	expr, err := ParseExpression(`a or not b and c|length > 1 or d`)
	testutil.FailErr(t, "ParseExpression failed", err)
	var atoms []string
	for _, atom := range expr.Atoms() {
		atoms = append(atoms, atom.Source())
	}
	if want := []string{"a", "b", "c|length > 1", "d"}; !slices.Equal(atoms, want) {
		t.Fatalf("atoms = %q, want %q", atoms, want)
	}
	for _, tc := range []struct {
		values map[string]bool
		want   bool
	}{
		{map[string]bool{}, false},
		{map[string]bool{"a": true}, true},
		{map[string]bool{"c|length > 1": true}, true},
		{map[string]bool{"b": true, "c|length > 1": true}, false},
		{map[string]bool{"d": true}, true},
		{map[string]bool{"b": true, "d": true}, false},
	} {
		if got := expr.Decide(tc.values); got != tc.want {
			t.Errorf("Decide(%v) = %v, want %v", tc.values, got, tc.want)
		}
	}
}

func TestExprIdentifiersAndOperatorDetection(t *testing.T) {
	for _, tc := range []struct {
		src    string
		idents []string
		ops    bool
	}{
		{`a.b == c|default:d and a.b`, []string{"a.b", "c", "d"}, true},
		{`-n + m`, []string{"n", "m"}, true},
		{`True`, []string{"True"}, false},
		{`a.0|default:b`, []string{"a.0", "b"}, false},
		{`a`, []string{"a"}, false},
		{`a|length`, []string{"a"}, false},
		{`"s"`, nil, false},
		{`true`, nil, true},
	} {
		expr, err := ParseExpression(tc.src)
		if err != nil {
			t.Fatalf("ParseExpression(%q): %v", tc.src, err)
		}
		if got := expr.Identifiers(); !slices.Equal(got, tc.idents) {
			t.Errorf("Identifiers(%q) = %q, want %q", tc.src, got, tc.idents)
		}
		if got := expr.HasLogicalOrCompare(); got != tc.ops {
			t.Errorf("HasLogicalOrCompare(%q) = %v, want %v", tc.src, got, tc.ops)
		}
	}
}

func TestParseTemplateTreeBuildsEveryNodeKind(t *testing.T) {
	src := `{% autoescape off %}{% with a=x b = y %}{% with items as rows %}` +
		`{% for k, v in rows reversed %}{{ k }}{% empty %}none{% endfor %}` +
		`{% endwith %}{% endwith %}{% endautoescape %}{% templatetag openblock %}` +
		`{% include "p.md" with n=1 only %}{% include 'q.md' if_exists %}{% include "r.md" only %}` +
		`{% include "s.md" if_exists with m=a %}{% set z = a|default:"d" %}` +
		`{# {% bogus %} #}{% comment %}{% bogus %}{% endcomment %}` +
		"\n{% for x in xs sorted %}{% if x %}{{ x }}{% endif %}{% endfor %}tail"
	tree, err := ParseTemplateTree(src)
	if err != nil {
		t.Fatalf("ParseTemplateTree: %v", err)
	}
	kinds := func(nodes []*TemplateNode) []NodeKind {
		var out []NodeKind
		for _, n := range nodes {
			out = append(out, n.Kind)
		}
		return out
	}
	want := []NodeKind{NodeWith, NodeText, NodeInclude, NodeInclude, NodeInclude, NodeInclude, NodeSet, NodeText, NodeFor, NodeText}
	if got := kinds(tree.Nodes); !slices.Equal(got, want) {
		t.Fatalf("root kinds = %v, want %v", got, want)
	}
	bindings := func(n *TemplateNode) []string {
		var out []string
		for _, b := range n.Bindings {
			out = append(out, b.Name+"="+b.Expr.Source())
		}
		return out
	}

	autoescape := tree.Nodes[0]
	if autoescape.RawExpr != "autoescape off" || len(autoescape.Body) != 1 {
		t.Fatalf("autoescape node = %+v", autoescape)
	}
	outer := autoescape.Body[0]
	if got := bindings(outer); !slices.Equal(got, []string{"a=x", "b=y"}) {
		t.Fatalf("with bindings = %q", got)
	}
	inner := outer.Body[0]
	if got := bindings(inner); !slices.Equal(got, []string{"rows=items"}) {
		t.Fatalf("with-as bindings = %q", got)
	}
	loop := inner.Body[0]
	if loop.Kind != NodeFor || !slices.Equal(loop.LoopVars, []string{"k", "v"}) || loop.RawExpr != "rows" {
		t.Fatalf("for node = %+v", loop)
	}
	if got := kinds(loop.Body); !slices.Equal(got, []NodeKind{NodeOutput}) {
		t.Fatalf("for body = %v", got)
	}
	if len(loop.EmptyBody) != 1 || loop.EmptyBody[0].Text != "none" {
		t.Fatalf("for empty body = %+v", loop.EmptyBody)
	}

	for i, inc := range []struct {
		ref      string
		bindings []string
	}{{"p.md", []string{"n=1"}}, {"q.md", nil}, {"r.md", nil}, {"s.md", []string{"m=a"}}} {
		node := tree.Nodes[2+i]
		if node.IncludeRef != inc.ref || !slices.Equal(bindings(node), inc.bindings) {
			t.Errorf("include %d = %q %q, want %q %q", i, node.IncludeRef, bindings(node), inc.ref, inc.bindings)
		}
	}
	if got := bindings(tree.Nodes[6]); !slices.Equal(got, []string{`z=a|default:"d"`}) {
		t.Fatalf("set bindings = %q", got)
	}
	sorted := tree.Nodes[8]
	if sorted.RawExpr != "xs" || sorted.Line != 2 || len(sorted.Body) != 1 || sorted.Body[0].Kind != NodeIf {
		t.Fatalf("sorted for node = %+v", sorted)
	}
	if tail := tree.Nodes[9]; tail.Text != "tail" || tail.Line != 2 {
		t.Fatalf("tail text = %+v", tail)
	}
}

func TestParseTemplateTreeFailsClosed(t *testing.T) {
	for name, src := range map[string]string{
		"empty output":         `{{ }}`,
		"empty control tag":    `{% %}`,
		"for without variable": `{% for in xs %}{% endfor %}`,
		"for without items":    `{% for x in %}{% endfor %}`,
		"for without in":       `{% for x xs %}{% endfor %}`,
		"for dotted variable":  `{% for x.y in xs %}{% endfor %}`,
		"for lex error":        `{% for "x %}{% endfor %}`,
		"for bad items":        `{% for x in a b %}{% endfor %}`,
		"stray empty":          `{% empty %}`,
		"stray endfor":         `{% endfor %}`,
		"empty with":           `{% with %}{% endwith %}`,
		"with lex error":       `{% with "a %}{% endwith %}`,
		"with bad as value":    `{% with a b as c %}{% endwith %}`,
		"with no binding":      `{% with a %}{% endwith %}`,
		"with bad value":       `{% with a=b c %}{% endwith %}`,
		"stray endwith":        `{% endwith %}`,
		"set without value":    `{% set x %}`,
		"set quoted name":      `{% set "x" = 1 %}`,
		"set dotted name":      `{% set a.b = 1 %}`,
		"set comparison":       `{% set x == 1 %}`,
		"set empty value":      `{% set x = %}`,
		"set bad value":        `{% set x = a b %}`,
		"dynamic include":      `{% include target %}`,
		"include clause":       `{% include "x" foo %}`,
		"include bare with":    `{% include "x" with %}`,
		"include no binding":   `{% include "x" with a %}`,
		"include bad value":    `{% include "x" with a=b c= %}`,
		"autoescape mode":      `{% autoescape maybe %}{% endautoescape %}`,
		"stray endautoescape":  `{% endautoescape %}`,
		"empty if":             `{% if %}{% endif %}`,
		"elif after else":      `{% if a %}{% else %}{% elif b %}{% endif %}`,
		"bad elif":             `{% if a %}{% elif b c %}{% endif %}`,
		"else arguments":       `{% if a %}{% else x %}{% endif %}`,
		"stray else":           `{% else %}`,
		"duplicate else":       `{% if a %}{% else %}{% else %}{% endif %}`,
		"stray endif":          `{% endif %}`,
		"unknown tag":          `{% custom %}`,
		"unclosed for":         `{% for x in xs %}`,
	} {
		if _, err := ParseTemplateTree(src); err == nil {
			t.Errorf("%s: ParseTemplateTree(%q) succeeded, want an error", name, src)
		}
	}
}
