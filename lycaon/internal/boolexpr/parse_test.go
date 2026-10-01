package boolexpr

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestParseOperatorPrecedence(t *testing.T) {
	// not binds tighter than and; and tighter than or.
	tree, err := Parse("a or b and not c")
	testutil.FailErr(t, "Parse failed", err)
	or, ok := tree.(Or)
	if !ok {
		t.Fatalf("root = %T", tree)
	}
	if _, ok := or.Left.(Ident); !ok {
		t.Fatalf("left = %T", or.Left)
	}
	and, ok := or.Right.(And)
	if !ok {
		t.Fatalf("right = %T", or.Right)
	}
	if _, ok := and.Right.(Not); !ok {
		t.Fatalf("and.right = %T", and.Right)
	}
}

func TestEvalDeMorganMetamorphic(t *testing.T) {
	env := func(name string) bool {
		switch name {
		case "stub_valid", "tool_is_state":
			return true
		default:
			return false
		}
	}
	a, err := Parse("not (stub_valid and tool_is_state)")
	testutil.FailErr(t, "Parse failed", err)
	b, err := Parse("not stub_valid or not tool_is_state")
	testutil.FailErr(t, "Parse failed", err)
	if Eval(a, env) != Eval(b, env) {
		t.Fatal("De Morgan equivalence failed for sample env")
	}
}

func TestParseRejectsInvalidInput(t *testing.T) {
	for _, expr := range []string{"", "1 + 2", "a = b"} {
		if _, err := Parse(expr); err == nil {
			t.Errorf("Parse(%q) want error", expr)
		}
	}
}

// A condition id carries whatever its parameterized value contains, so
// identifier text is not charset-restricted.
func TestParseAcceptsParameterizedIdentText(t *testing.T) {
	for _, expr := range []string{"var_equals:plan.status,draft", "a.b", "a[b]", "path_is:src/**"} {
		node, err := Parse(expr)
		testutil.FailErr(t, "Parse failed", err)
		ident, ok := node.(Ident)
		if !ok || ident.Name != expr {
			t.Fatalf("Parse(%q) = %#v want a single Ident", expr, node)
		}
	}
}

// ValidateSimpleIdents serves callers whose environment cannot reject an
// unknown name.
func TestValidateSimpleIdentsRejectsRichText(t *testing.T) {
	for _, expr := range []string{"a.b", "a[b]", "phase|length", "context.secrets"} {
		node, err := Parse(expr)
		testutil.FailErr(t, "Parse failed", err)
		if err := ValidateSimpleIdents(node); err == nil {
			t.Errorf("ValidateSimpleIdents(%q) want error", expr)
		}
	}
	node, err := Parse("stub_valid and var_truthy:x")
	testutil.FailErr(t, "Parse failed", err)
	if err := ValidateSimpleIdents(node); err != nil {
		t.Fatalf("plain names must pass: %v", err)
	}
}

// CollectIdents names exactly what Eval looks up, so validation and evaluation
// resolve the same names.
func TestCollectIdentsMatchesEvalLookup(t *testing.T) {
	node, err := Parse("var_equals:plan.status,draft and not stub_valid")
	testutil.FailErr(t, "Parse failed", err)
	var queried []string
	Eval(node, func(name string) bool {
		queried = append(queried, name)
		return true
	})
	idents := CollectIdents(node)
	if len(idents) != len(queried) {
		t.Fatalf("CollectIdents = %v, Eval queried %v", idents, queried)
	}
	for i := range idents {
		if idents[i] != queried[i] {
			t.Fatalf("CollectIdents[%d] = %q, Eval queried %q", i, idents[i], queried[i])
		}
	}
}

// Function-call syntax is not part of the grammar.
func TestParseRejectsCallForm(t *testing.T) {
	if _, err := Parse(`provider_hint_emitted("CODE")`); err == nil {
		t.Fatal("call form must not parse")
	}
}
