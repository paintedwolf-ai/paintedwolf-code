package pongoplain

import (
	"reflect"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// oracleAssignments are the variable sets every oracle expression is decided
// under; they separate each pair of groupings the table exercises.
var oracleAssignments = []map[string]any{
	{"a": true, "b": false, "c": true, "n": 2, "m": 3, "k": 1, "s": "abc", "xs": []any{1, 2, 3}},
	{"a": false, "b": true, "c": false, "n": 0, "m": 0, "k": 2, "s": "", "xs": []any{}},
	{"a": true, "b": true, "c": false, "n": 3, "m": 2, "k": 4, "s": "z", "xs": []any{3}},
	{"a": false, "b": false, "c": true, "n": 1, "m": 1, "k": 3, "s": "ab", "xs": []any{1}},
}

func pongoRender(src string, vars map[string]any) (string, error) {
	tpl, err := Compile(src)
	if err != nil {
		return "", err
	}
	return ExecuteDetached(tpl, vars)
}

// pongoDecides reports pongo2's own if-decision for expr under vars.
func pongoDecides(t *testing.T, expr string, vars map[string]any) bool {
	t.Helper()
	out, err := pongoRender("{% if "+expr+" %}1{% else %}0{% endif %}", vars)
	if err != nil {
		t.Fatalf("pongo2 decide %q under %v: %v", expr, vars, err)
	}
	return out == "1"
}

// parenthesised prints e with every compound operand in parentheses, so
// pongo2 evaluates exactly the tree e describes.
func parenthesised(e *Expr) string {
	switch e.Kind {
	case ExprNot:
		return "not (" + parenthesised(e.Left) + ")"
	case ExprNeg:
		return "-(" + parenthesised(e.Left) + ")"
	case ExprAnd, ExprOr, ExprCompare, ExprArith:
		return "(" + parenthesised(e.Left) + ") " + e.Op + " (" + parenthesised(e.Right) + ")"
	case ExprFilter, ExprIdent, ExprLiteral:
	}
	return e.Source()
}

func TestParsedTreeMatchesPongoEvaluation(t *testing.T) {
	for _, tc := range []struct {
		src   string
		atoms []string
	}{
		{`a and b or c`, []string{"a", "b", "c"}},
		{`a or b and c`, []string{"a", "b", "c"}},
		{`not a or b and not c`, []string{"a", "b", "c"}},
		{`!a || b && c`, []string{"a", "b", "c"}},
		{`(a and b) or not (a and c)`, []string{"a", "b", "a", "c"}},
		{`not (a or b) and c`, []string{"a", "b", "c"}},
		{`not (not a)`, []string{"a"}},
		{`not n == m`, []string{"not n == m"}},
		{`not n == m and a`, []string{"not n == m", "a"}},
		{`n == m or k == 2 and not a`, []string{"n == m", "k == 2", "a"}},
		{`n == m == a`, []string{"n == m == a"}},
		{`(n == m) == a`, []string{"(n == m) == a"}},
		{`n < m < k`, []string{"n < m < k"}},
		{`n <> m`, []string{"n != m"}},
		{`n in xs or s in "abc" and b`, []string{"n in xs", `s in "abc"`, "b"}},
		{`n + 1 in xs`, []string{"n + 1 in xs"}},
		{`a == n in xs`, []string{"a == n in xs"}},
		{`n + m * k == 7`, []string{"n + m * k == 7"}},
		{`n - m - k`, []string{"n - m - k"}},
		{`n - (m - k)`, []string{"n - (m - k)"}},
		{`n * m % k + n`, []string{"n * m % k + n"}},
		{`n ^ m ^ k`, []string{"n ^ m ^ k"}},
		{`(n ^ m) ^ k`, []string{"(n ^ m) ^ k"}},
		{`-n + m > 0 and -n * m < 0`, []string{"-n + m > 0", "-n * m < 0"}},
		{`not n + m`, []string{"not n + m"}},
		{`-not n`, []string{"-not n"}},
		{`+n == 2.5 or n >= 2`, []string{"n == 2.5", "n >= 2"}},
		{`s|length > n and xs|length`, []string{"s|length > n", "xs|length"}},
		{`s|default:"z" == "z" or not a`, []string{`s|default:"z" == "z"`, "a"}},
		{`'q"\\' in s|default:'q"\\'`, []string{`"q\"\\" in s|default:"q\"\\"`}},
	} {
		expr, err := ParseExpression(tc.src)
		if err != nil {
			t.Errorf("ParseExpression(%q): %v", tc.src, err)
			continue
		}
		var atoms []string
		for _, atom := range expr.Atoms() {
			atoms = append(atoms, atom.Source())
		}
		if !slices.Equal(atoms, tc.atoms) {
			t.Errorf("atoms of %q = %q, want %q", tc.src, atoms, tc.atoms)
		}
		reparsed, err := ParseExpression(expr.Source())
		if err != nil || !reflect.DeepEqual(reparsed, expr) {
			t.Errorf("Source %q of %q does not reparse to the same tree: %v", expr.Source(), tc.src, err)
		}
		for _, vars := range oracleAssignments {
			want, wantErr := pongoRender("{{ "+tc.src+" }}", vars)
			for _, printed := range []string{expr.Source(), parenthesised(expr)} {
				got, gotErr := pongoRender("{{ "+printed+" }}", vars)
				if got != want || (gotErr == nil) != (wantErr == nil) {
					t.Errorf("pongo2 renders %q as %q (%v), but %q as %q (%v) under %v", tc.src, want, wantErr, printed, got, gotErr, vars)
				}
			}
			if wantErr != nil {
				continue
			}
			truths := map[string]bool{}
			for _, atom := range atoms {
				truths[atom] = pongoDecides(t, atom, vars)
			}
			if got, want := expr.Decide(truths), pongoDecides(t, tc.src, vars); got != want {
				t.Errorf("Decide(%q) over atoms %v = %v, pongo2 decides %v under %v", tc.src, truths, got, want, vars)
			}
		}
	}
}

func TestObservedConditionsRecomposeUnderPongoGrammar(t *testing.T) {
	src := `{% if a and b or not n == m and c %}1{% elif -n + m > 0 or a and not c %}2{% endif %}`
	tpl, err := Compile(src)
	testutil.FailErr(t, "Compile failed", err)
	rec := &BranchRecorder{ObserveConditions: true}
	ActiveBranchRecorder.Store(rec)
	defer ActiveBranchRecorder.Store(nil)
	for _, vars := range oracleAssignments {
		if _, err := ExecuteDetached(tpl, vars); err != nil {
			testutil.FailErr(t, "ExecuteDetached failed", err)
		}
	}
	if len(rec.Conditions) != 2 {
		t.Fatalf("conditions = %+v, want the if and elif arms", rec.Conditions)
	}
	for _, hit := range rec.Conditions {
		for _, v := range hit.Vectors {
			if !v.Recomposes {
				t.Errorf("%s: atoms %q with values %v do not recompose decision %v", hit.Source, hit.Atoms, v.Atoms, v.Decision)
			}
		}
	}
}

func TestParserRefusesFormsPongoRefuses(t *testing.T) {
	for _, src := range []string{
		`x if c else y`,
		`x if c`,
		`a not in b`,
		`not not a`,
		`not -a`,
		`a - -1`,
		`a in b == c`,
		`x|default:(a or b)`,
		`x|default:-1`,
		`(a)|length`,
		`"a\nb"`,
		`'it\'s'`,
		`1.x`,
		`a.in`,
		`a|in`,
		`export`,
	} {
		if expr, err := ParseExpression(src); err == nil {
			t.Errorf("ParseExpression(%q) = %s, want an error", src, expr.Source())
		}
		if _, err := Compile("{% if " + src + " %}{% endif %}"); err == nil {
			t.Errorf("pongo2 accepts %q; the table must list forms pongo2 refuses", src)
		}
	}
}
