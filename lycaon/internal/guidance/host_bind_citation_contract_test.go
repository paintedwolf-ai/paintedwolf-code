package guidance_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A matching excerpt can recover a stale handle.
func TestBindArm_CloseoutStaleHandleLands(t *testing.T) {
	ev := bindLedger(readSpec{"app.ts", 12, "func NewBoard() {"})
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "The board is initialized during setup.",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{
			{Evidence: "leg-a:grep#1", Excerpt: "func NewBoard() {"},
		},
	}
	eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{}, "implement_synthesis", report, guidance.CloseoutEvidence{Ledger: ev})
	if eval.Code != "" {
		t.Fatalf("eval.Code = %q, want no rejection: a bound handle must not raise SYNTH_HANDLE_NOT_IN_LEGS", eval.Code)
	}
	if eval.BindAdvisoryCount != 1 {
		t.Fatalf("BindAdvisoryCount = %d, want 1 (advisories=%v)", eval.BindAdvisoryCount, eval.BindAdvisoryTokens)
	}
}

// Excerpt recovery keeps an explicit path constraint.
func TestBindArm_WrongPathDoesNotBind(t *testing.T) {
	ev := bindLedger(readSpec{"app.ts", 12, "func NewBoard() {"})
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "Board setup.",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{
			{Path: "other.ts", Line: 3, Excerpt: "func NewBoard() {"},
		},
	}
	eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{}, "implement_synthesis", report, guidance.CloseoutEvidence{Ledger: ev})
	if eval.Code != guidance.SynthHandleNotInLegsCode {
		t.Fatalf("eval.Code = %q, want reject for a wrong-path citation", eval.Code)
	}
}

// The bind resolver accepts only typed citations and the ledger.
func TestBindResolverTakesNoNarrative(t *testing.T) {
	src, err := os.ReadFile("host_bind_citation.go")
	testutil.FailErr(t, "read host_bind_citation.go", err)
	for _, banned := range []string{"WorkerNarrativeInput", ".Synthesis", ".Brief", ".Narrative"} {
		if strings.Contains(string(src), banned) {
			t.Fatalf("bind resolver source references %q — bind must be machine-state only", banned)
		}
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "host_bind_citation.go", src, 0)
	testutil.FailErr(t, "parse host_bind_citation.go", err)

	allowed := map[string]struct{}{
		"string":              {},
		"WorkerFindingInput":  {},
		"evidence.Ledger":     {},
		"evidence.Resolution": {},
	}
	targets := map[string]bool{"bindLooseCitation": true, "ledgerRecordsMatching": true}
	seen := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name == nil || !targets[fn.Name.Name] {
			return true
		}
		seen[fn.Name.Name] = true
		for _, p := range fn.Type.Params.List {
			ts := types.ExprString(p.Type)
			if _, ok := allowed[ts]; !ok {
				t.Fatalf("%s param type %q not in the typed-citation/ledger allowlist", fn.Name.Name, ts)
			}
		}
		return true
	})
	for name := range targets {
		if !seen[name] {
			t.Fatalf("governance target %s not found in host_bind_citation.go", name)
		}
	}
}

// MC-INV-06: publication decides from the parse and the ledger. The note's text reaches
// the audit only through the parser and the bare-JSON check, so no prose can steer it.
func TestMidTurnAuditReadsTextOnlyThroughTheParser(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "mid_turn_note.go", nil, 0)
	testutil.FailErr(t, "parse mid_turn_note.go", err)
	var audit *ast.FuncDecl
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "AuditMidTurnNote" {
			audit = fn
		}
	}
	if audit == nil {
		t.Fatal("AuditMidTurnNote not found")
	}
	allowed := map[string]bool{"ParseCloseoutDraft": true, "IsBareJSONResponse": true}
	reads := 0
	ast.Inspect(audit.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			name, _ := node.Fun.(*ast.Ident)
			for _, arg := range node.Args {
				if selector, ok := arg.(*ast.SelectorExpr); ok && selector.Sel.Name == "Text" {
					reads++
					if name == nil || !allowed[name.Name] {
						t.Fatalf("note text reaches %s", types.ExprString(node.Fun))
					}
				}
			}
		case *ast.AssignStmt:
			for _, rhs := range node.Rhs {
				if selector, ok := rhs.(*ast.SelectorExpr); ok && selector.Sel.Name == "Text" {
					t.Fatalf("note text escapes into a local")
				}
			}
		}
		return true
	})
	if reads != 2 {
		t.Fatalf("note text is read %d times, want the parser and the bare-JSON check", reads)
	}
}
