package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var reDirectErrNoRowsEq = regexp.MustCompile(`\berr\s*==\s*sql\.ErrNoRows\b|\berr\s*!=\s*sql\.ErrNoRows\b`)

// reMattnImport / reSqlite3Driver enforce the single repo-wide SQLite driver.
var (
	reMattnImport   = regexp.MustCompile(`github\.com/mattn/go-sqlite3`)
	reSqlite3Driver = regexp.MustCompile(`sql\.Open\(\s*"sqlite3"`)
	// reSQLInSprintf flags a fmt.Sprintf format literal that builds SQL — value
	// interpolation into SQL is forbidden; bind with ? placeholders instead.
	reSQLInSprintf = regexp.MustCompile(`(?i)(select\s|insert\s+into|update\s|delete\s+from|\swhere\s|\svalues\s*\()`)
)

// sqlErrNoRowsContractExempt marks production files allowed to reference sql.ErrNoRows directly.
var sqlErrNoRowsContractExempt = map[string]bool{
	"lycaon/internal/db/sqlerr.go": true,
}

// contextAPIExemptPrefixes lists subsystems that use non-Context SQL calls.
// internal/webindex runs on its own SQLite file from one background writer with
// no request context.
var contextAPIExemptPrefixes = []string{
	"lycaon/internal/webindex/",
}

func TestProductionGoDoesNotReturnSQLErrNoRows(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	internal := filepath.Join(root, "lycaon", "internal")
	var violations []string
	err := contractcheck.WalkFiles(internal, map[string]struct{}{".go": {}}, true, func(path string, data []byte) error {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, ".sql.go") || sqlErrNoRowsContractExempt[rel] {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, data, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			ret, ok := n.(*ast.ReturnStmt)
			if !ok {
				return true
			}
			for _, res := range ret.Results {
				if isSQLErrNoRowsExpr(res) {
					pos := fset.Position(res.Pos())
					violations = append(violations, rel+":"+strconv.Itoa(pos.Line)+": return sql.ErrNoRows — map to a typed domain error or (nil, nil)")
				}
			}
			return true
		})
		return nil
	})
	contractcheck.FailErr(t, "walk internal for return sql.ErrNoRows", err)
	if len(violations) > 0 {
		t.Fatalf("production Go must not return sql.ErrNoRows:\n%s", strings.Join(violations, "\n"))
	}
}

func TestProductionGoUsesErrorsIsForErrNoRows(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	internal := filepath.Join(root, "lycaon", "internal")
	var violations []string
	err := contractcheck.WalkFiles(internal, map[string]struct{}{".go": {}}, true, func(path string, data []byte) error {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, ".sql.go") || sqlErrNoRowsContractExempt[rel] {
			return nil
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if reDirectErrNoRowsEq.MatchString(line) {
				violations = append(violations, rel+":"+strconv.Itoa(i+1)+": use errors.Is(err, sql.ErrNoRows) or db.IsNoRows(err)")
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk internal for err == sql.ErrNoRows", err)
	if len(violations) > 0 {
		t.Fatalf("direct sql.ErrNoRows equality checks are forbidden:\n%s", strings.Join(violations, "\n"))
	}
}

func isSQLErrNoRowsExpr(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "sql" && sel.Sel.Name == "ErrNoRows"
}

// TestSingleSQLiteDriver keeps the whole repo on the pure-Go modernc.org/sqlite
// driver ("sqlite"). No file may import mattn/go-sqlite3 or open the CGO
// "sqlite3" driver.
func TestSingleSQLiteDriver(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonDir := filepath.Join(root, "lycaon")
	var violations []string
	err := contractcheck.WalkFiles(lycaonDir, map[string]struct{}{".go": {}}, false, func(path string, data []byte) error {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		// This enforcement file necessarily contains the forbidden literals in
		// its patterns and messages; skip scanning itself.
		if strings.HasSuffix(rel, "sql_query_conventions_contract_test.go") {
			return nil
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if reMattnImport.MatchString(line) {
				violations = append(violations, rel+":"+strconv.Itoa(i+1)+": imports mattn/go-sqlite3 — use modernc.org/sqlite")
			}
			if reSqlite3Driver.MatchString(line) {
				violations = append(violations, rel+":"+strconv.Itoa(i+1)+`: sql.Open("sqlite3", …) — use the "sqlite" (modernc) driver`)
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk lycaon for sqlite driver", err)
	if len(violations) > 0 {
		t.Fatalf("repo must use only modernc.org/sqlite:\n%s", strings.Join(violations, "\n"))
	}
}

// TestProductionSQLUsesContextAPIs forbids the non-Context database/sql methods
// (Exec/Query/QueryRow with a literal SQL string) in request-path production Go;
// the webindex background subsystem is exempt (contextAPIExemptPrefixes).
func TestProductionSQLUsesContextAPIs(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	internal := filepath.Join(root, "lycaon", "internal")
	var violations []string
	err := contractcheck.WalkFiles(internal, map[string]struct{}{".go": {}}, true, func(path string, data []byte) error {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, ".sql.go") {
			return nil
		}
		for _, prefix := range contextAPIExemptPrefixes {
			if strings.HasPrefix(rel, prefix) {
				return nil
			}
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, data, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "Exec", "Query", "QueryRow":
			default:
				return true
			}
			// DB exec/query pass a SQL string literal first; domain methods like
			// coord.Query(ctx, …) and url.Query() do not, so they don't match.
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				pos := fset.Position(call.Pos())
				violations = append(violations, rel+":"+strconv.Itoa(pos.Line)+": use "+sel.Sel.Name+"Context(ctx, …) instead of "+sel.Sel.Name)
			}
			return true
		})
		return nil
	})
	contractcheck.FailErr(t, "walk internal for non-Context SQL", err)
	if len(violations) > 0 {
		t.Fatalf("request-path SQL must use Context APIs:\n%s", strings.Join(violations, "\n"))
	}
}

// TestNoSQLStringInterpolation forbids building SQL with fmt.Sprintf. Store-layer
// dynamic shape (IN lists, optional filters) comes from sqlc.slice / sqlc.arg
// toggles; the remaining hand-written SQL composes constant fragments plus
// db.INPlaceholders with bound ? parameters — never by interpolating values into
// the SQL text.
func TestNoSQLStringInterpolation(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	internal := filepath.Join(root, "lycaon", "internal")
	var violations []string
	err := contractcheck.WalkFiles(internal, map[string]struct{}{".go": {}}, true, func(path string, data []byte) error {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, ".sql.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, data, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "fmt" || sel.Sel.Name != "Sprintf" {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if reSQLInSprintf.MatchString(lit.Value) {
				pos := fset.Position(call.Pos())
				violations = append(violations, rel+":"+strconv.Itoa(pos.Line)+": fmt.Sprintf builds SQL — bind values with ? placeholders (db.INPlaceholders for IN lists)")
			}
			return true
		})
		return nil
	})
	contractcheck.FailErr(t, "walk internal for SQL in Sprintf", err)
	if len(violations) > 0 {
		t.Fatalf("SQL must not be built with fmt.Sprintf:\n%s", strings.Join(violations, "\n"))
	}
}

// storeLayerSQLPackages persist through generated database methods.
var storeLayerSQLPackages = []string{
	"lycaon/internal/call/",
	"lycaon/internal/delegation/",
	"lycaon/internal/findings/",
	"lycaon/internal/hitl/",
	"lycaon/internal/progress/",
	"lycaon/internal/scan/",
	"lycaon/internal/session/store/",
	"lycaon/internal/worker/",
	"lycaon/internal/workflow/",
}

// These store packages persist through generated query methods.
func TestStoreLayerHasNoHandWrittenSQL(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var violations []string
	for _, pkg := range storeLayerSQLPackages {
		dir := filepath.Join(root, filepath.FromSlash(pkg))
		err := contractcheck.WalkFiles(dir, map[string]struct{}{".go": {}}, true, func(path string, data []byte) error {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if strings.HasSuffix(rel, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, data, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "ExecContext", "QueryContext", "QueryRowContext":
				default:
					return true
				}
				// Generated calls take the query as an identifier; a string
				// literal here is inline SQL written by hand.
				sqlArg := call.Args[len(call.Args)-1]
				if len(call.Args) >= 2 {
					sqlArg = call.Args[1]
				}
				if lit, ok := sqlArg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					pos := fset.Position(call.Pos())
					violations = append(violations, rel+":"+strconv.Itoa(pos.Line)+": inline SQL in a store package — add the query to internal/db/queries/ and call the generated method")
				}
				return true
			})
			return nil
		})
		contractcheck.FailErr(t, "walk "+pkg+" for hand-written SQL", err)
	}
	if len(violations) > 0 {
		t.Fatalf("store-layer SQL must go through sqlc:\n%s", strings.Join(violations, "\n"))
	}
}
