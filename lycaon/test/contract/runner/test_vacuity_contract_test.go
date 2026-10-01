package contract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestRepositoryScansCannotPassOnAnEmptyScan(t *testing.T) {
	t.Parallel()
	var violations []string
	examined := 0

	walkTestFiles(t, contractcheck.RepoRoot(t), func(pkg string, fset *token.FileSet, file *ast.File, _ []byte) {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			examined++
			if scanPassesWhenEmpty(fn) {
				violations = append(violations, fmt.Sprintf(
					"%s %s (%s): asserts only inside a loop over a set it discovers, and never requires that set to be non-empty — a rename that empties the scan turns this green",
					pkg, fn.Name.Name, fset.Position(fn.Pos())))
			}
		}
	})

	if examined == 0 {
		t.Fatal("no test functions examined; the scan is not testing anything")
	}
	contractcheck.FailViolations(t, "repository scans that pass when the scan finds nothing", violations)
}

func scanPassesWhenEmpty(fn *ast.FuncDecl) bool {
	discovers := false
	grown := map[string]bool{}   // variables grown inside the discovery
	guarded := map[string]bool{} // variables whose empty case fails the test

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			switch calleeName(v.Fun) {
			case "WalkDir", "Walk", "ReadDir", "Glob":
				discovers = true
			case "append":
				if len(v.Args) > 0 {
					if id, ok := v.Args[0].(*ast.Ident); ok {
						grown[id.Name] = true
					}
				}
			}
		case *ast.AssignStmt:
			for _, lhs := range v.Lhs {
				if ix, ok := lhs.(*ast.IndexExpr); ok {
					if id, ok := ix.X.(*ast.Ident); ok {
						grown[id.Name] = true
					}
				}
			}
		case *ast.IfStmt:
			if containsFailure(v.Body) {
				for _, name := range emptySetConditionNames(v.Cond) {
					guarded[name] = true
				}
			}
		}
		return true
	})
	if !discovers || len(grown) == 0 {
		return false
	}

	guardedByLoop := 0
	var walk func(n ast.Node, overGrown bool)
	walk = func(n ast.Node, overGrown bool) {
		if n == nil {
			return
		}
		switch v := n.(type) {
		case *ast.RangeStmt:
			inner := overGrown
			if id, ok := v.X.(*ast.Ident); ok && grown[id.Name] && !guarded[id.Name] {
				inner = true
			}
			walk(v.Body, inner)
			return
		case *ast.CallExpr:
			switch calleeName(v.Fun) {
			case "Error", "Errorf", "Fatal", "Fatalf", "FailNow", "failErr", "failViolations", "failSetEqual":
				if overGrown {
					guardedByLoop++
				}
			}
		}
		for _, child := range directChildren(n) {
			walk(child, overGrown)
		}
	}
	walk(fn.Body, false)
	return guardedByLoop > 0
}

func emptySetConditionNames(expr ast.Expr) []string {
	binary, ok := expr.(*ast.BinaryExpr)
	if !ok {
		return nil
	}
	if binary.Op == token.LOR {
		return append(emptySetConditionNames(binary.X), emptySetConditionNames(binary.Y)...)
	}
	if name, ok := lenIdentifier(binary.X); ok {
		if emptyLengthComparison(binary.Op, binary.Y) {
			return []string{name}
		}
		return nil
	}
	name, ok := lenIdentifier(binary.Y)
	if !ok {
		return nil
	}
	switch binary.Op {
	case token.EQL:
		if emptyLengthComparison(token.EQL, binary.X) {
			return []string{name}
		}
	case token.NEQ:
		if emptyLengthComparison(token.NEQ, binary.X) {
			return []string{name}
		}
	case token.GEQ:
		if emptyLengthComparison(token.LEQ, binary.X) {
			return []string{name}
		}
	case token.GTR:
		if emptyLengthComparison(token.LSS, binary.X) {
			return []string{name}
		}
	case token.LEQ:
		if emptyLengthComparison(token.GEQ, binary.X) {
			return []string{name}
		}
	case token.LSS:
		if emptyLengthComparison(token.GTR, binary.X) {
			return []string{name}
		}
	default:
	}
	return nil
}

func emptyLengthComparison(op token.Token, expr ast.Expr) bool {
	value, ok := integerLiteral(expr)
	if !ok {
		return false
	}
	switch op {
	case token.EQL:
		return value == 0
	case token.NEQ:
		return value != 0
	case token.LEQ:
		return value >= 0
	case token.LSS:
		return value > 0
	case token.GEQ:
		return value <= 0
	case token.GTR:
		return value < 0
	default:
	}
	return false
}

func lenIdentifier(expr ast.Expr) (string, bool) {
	call, ok := expr.(*ast.CallExpr)
	if !ok || calleeName(call.Fun) != "len" || len(call.Args) != 1 {
		return "", false
	}
	id, ok := call.Args[0].(*ast.Ident)
	if !ok {
		return "", false
	}
	return id.Name, true
}

func integerLiteral(expr ast.Expr) (int, bool) {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.INT {
		return 0, false
	}
	value, err := strconv.Atoi(literal.Value)
	return value, err == nil
}

func containsFailure(node ast.Node) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch calleeName(call.Fun) {
		case "Error", "Errorf", "Fatal", "Fatalf", "FailNow", "failErr", "failViolations", "failSetEqual":
			found = true
		}
		return true
	})
	return found
}

func directChildren(n ast.Node) []ast.Node {
	var out []ast.Node
	ast.Inspect(n, func(c ast.Node) bool {
		if c == nil || c == n {
			return true
		}
		out = append(out, c)
		return false
	})
	return out
}

func TestScanVacuityDetectorRecognizesItsOwnShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		want bool
		src  string
	}{{
		name: "vacuous when the walk finds nothing",
		want: true,
		src: `
			var found []string
			_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				found = append(found, p)
				return nil
			})
			for _, f := range found {
				if bad(f) {
					t.Errorf("bad %s", f)
				}
			}`,
	}, {
		name: "guarded by a length check",
		want: false,
		src: `
			var found []string
			_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				found = append(found, p)
				return nil
			})
			if len(found) == 0 {
				t.Fatal("scan found nothing")
			}
			for _, f := range found {
				if bad(f) {
					t.Errorf("bad %s", f)
				}
			}`,
	}, {
		name: "reversed nonempty comparison does not guard",
		want: true,
		src: `
			var found []string
			_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				found = append(found, p)
				return nil
			})
			if 0 < len(found) {
				t.Fatal("not empty")
			}
			for _, f := range found {
				if bad(f) {
					t.Errorf("bad %s", f)
				}
			}`,
	}, {
		name: "reversed empty comparison guards",
		want: false,
		src: `
			var found []string
			_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				found = append(found, p)
				return nil
			})
			if 0 >= len(found) {
				t.Fatal("scan found nothing")
			}
			for _, f := range found {
				if bad(f) {
					t.Errorf("bad %s", f)
				}
			}`,
	}, {
		name: "meaningless length comparison does not guard",
		want: true,
		src: `
			var found []string
			_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				found = append(found, p)
				return nil
			})
			_ = len(found) == 0
			for _, f := range found {
				if bad(f) {
					t.Errorf("bad %s", f)
				}
			}`,
	}, {
		name: "non-failing empty branch does not guard",
		want: true,
		src: `
			var found []string
			_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				found = append(found, p)
				return nil
			})
			if len(found) == 0 {
				t.Log("scan found nothing")
			}
			for _, f := range found {
				if bad(f) {
					t.Errorf("bad %s", f)
				}
			}`,
	}, {
		name: "asserts presence over a literal expectation list",
		want: false,
		src: `
			seen := map[string]bool{}
			_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				seen[p] = true
				return nil
			})
			for _, want := range []string{"a", "b"} {
				if !seen[want] {
					t.Errorf("missing %s", want)
				}
			}`,
	}, {
		name: "no discovery at all",
		want: false,
		src: `
			for _, f := range []string{"a"} {
				if bad(f) {
					t.Errorf("bad %s", f)
				}
			}`,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := "package p\nfunc TestX(t *testing.T) {\n" + tc.src + "\n}\n"
			file, err := parser.ParseFile(token.NewFileSet(), "synthetic_test.go", src, 0)
			contractcheck.FailErr(t, "parse synthetic test", err)
			fn, ok := file.Decls[0].(*ast.FuncDecl)
			if !ok {
				t.Fatal("synthetic source did not yield a function declaration")
			}
			if got := scanPassesWhenEmpty(fn); got != tc.want {
				t.Fatalf("scanPassesWhenEmpty = %v, want %v", got, tc.want)
			}
		})
	}
}
