package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Process starts carry launch attribution through the typed boundary.
func TestProcessLaunchesUseTheTypedDoor(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	internal := filepath.Join(root, "lycaon", "internal")
	fset := token.NewFileSet()
	var findings []string
	err := filepath.WalkDir(internal, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		rel, _ := filepath.Rel(root, path)
		slashRel := filepath.ToSlash(rel)
		aliases := importAliases(f)
		prepared := preparedCommandNames(f, aliases)
		emptyErrorReturns := emptyOptionsErrorReturns(f)
		allowRaw := strings.HasPrefix(slashRel, "lycaon/internal/exec/") ||
			strings.HasPrefix(slashRel, "lycaon/internal/confine/")
		allowLowLevel := allowRaw || strings.HasPrefix(slashRel, "lycaon/internal/launchguard/")
		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CallExpr:
				if !allowRaw && (isRawProcessStart(node.Fun, aliases) || isRawPreparedStart(node.Fun, prepared)) {
					findings = append(findings, positionFinding(fset, node.Pos(), rel, "raw os/exec process start"))
				}
				if !allowLowLevel && isLowLevelProcessStart(node.Fun, aliases) {
					findings = append(findings, positionFinding(fset, node.Pos(), rel, "direct low-level process launch"))
				}
				if isLauncherDirectStart(node.Fun, aliases) {
					findings = append(findings, positionFinding(fset, node.Pos(), rel, "direct go-rod launcher process start or leakless helper"))
				}
			case *ast.CompositeLit:
				if !allowLowLevel && isRawExecCmdLiteral(node.Type, aliases) {
					findings = append(findings, positionFinding(fset, node.Pos(), rel, "raw os/exec.Cmd literal"))
				}
				if isLaunchOptionsLiteral(node.Type, f.Name.Name, aliases) && !hasKey(node, "Launch") && !emptyErrorReturns[node] {
					findings = append(findings, positionFinding(fset, node.Pos(), rel, "process options omit Launch"))
				}
			}
			return true
		})
		return nil
	})
	contractcheck.FailErr(t, "scan process launches", err)
	if len(findings) != 0 {
		t.Fatalf("process launch door drift:\n  %s", strings.Join(findings, "\n  "))
	}
}

// Every engine launch entry point routes through the protected launch broker.
func TestEngineLaunchDoorsRouteThroughTheBroker(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for file, want := range map[string]string{
		"lycaon/internal/exec/pipeline.go":         "relayThroughBroker(cmd)",
		"lycaon/internal/exec/supervisor_linux.go": "relayThroughBroker(cmd)",
	} {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		contractcheck.FailErr(t, "read "+file, err)
		if !strings.Contains(string(raw), want) {
			t.Fatalf("%s does not route through %s", file, want)
		}
	}
}

func importAliases(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, spec := range f.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(path)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		out[name] = path
	}
	return out
}

func isRawProcessStart(expr ast.Expr, aliases map[string]string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || (sel.Sel.Name != "Command" && sel.Sel.Name != "CommandContext") {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && aliases[pkg.Name] == "os/exec"
}

func isLowLevelProcessStart(expr ast.Expr, aliases map[string]string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	switch aliases[pkg.Name] {
	case "os":
		return sel.Sel.Name == "StartProcess"
	case "syscall", "golang.org/x/sys/unix":
		return sel.Sel.Name == "ForkExec" || sel.Sel.Name == "StartProcess" || sel.Sel.Name == "Exec"
	default:
		return false
	}
}

func isRawExecCmdLiteral(expr ast.Expr, aliases map[string]string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Cmd" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && aliases[pkg.Name] == "os/exec"
}

func isLauncherDirectStart(expr ast.Expr, aliases map[string]string) bool {
	hasLauncher := false
	for _, path := range aliases {
		if path == "github.com/go-rod/rod/lib/launcher" {
			hasLauncher = true
			break
		}
	}
	if !hasLauncher {
		return false
	}
	sel, ok := expr.(*ast.SelectorExpr)
	return ok && (sel.Sel.Name == "Launch" || sel.Sel.Name == "Leakless")
}

func isLaunchOptionsLiteral(expr ast.Expr, pkg string, aliases map[string]string) bool {
	switch typ := expr.(type) {
	case *ast.Ident:
		return pkg == "exec" && (typ.Name == "ExecOpts" || typ.Name == "PTYOpts")
	case *ast.SelectorExpr:
		name, ok := typ.X.(*ast.Ident)
		if !ok {
			return false
		}
		path := aliases[name.Name]
		return path == "github.com/lycaon/lycaon/internal/exec" && (typ.Sel.Name == "ExecOpts" || typ.Sel.Name == "PTYOpts") ||
			path == "github.com/lycaon/lycaon/internal/hostcmd" && typ.Sel.Name == "Request"
	default:
		return false
	}
}

func hasKey(lit *ast.CompositeLit, want string) bool {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if ok && key.Name == want {
			return true
		}
	}
	return false
}

func positionFinding(fset *token.FileSet, pos token.Pos, rel, message string) string {
	return rel + ":" + strconv.Itoa(fset.Position(pos).Line) + ": " + message
}

// Prepared commands keep their actual start algorithm behind the registered
// lifecycle. Native Wait and specialized typed owners remain independent.
func preparedCommandNames(f *ast.File, aliases map[string]string) map[string]bool {
	names := map[string]bool{}
	ast.Inspect(f, func(node ast.Node) bool {
		assignment, ok := node.(*ast.AssignStmt)
		if !ok || len(assignment.Rhs) != 1 || len(assignment.Lhs) == 0 {
			return true
		}
		call, ok := assignment.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "PrepareCommand" {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok || aliases[pkg.Name] != "github.com/lycaon/lycaon/internal/exec" {
			return true
		}
		name, ok := assignment.Lhs[0].(*ast.Ident)
		if ok {
			names[name.Name] = true
		}
		return true
	})
	return names
}

func isRawPreparedStart(expr ast.Expr, names map[string]bool) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || (selector.Sel.Name != "Start" && selector.Sel.Name != "Run" && selector.Sel.Name != "Output" && selector.Sel.Name != "CombinedOutput") {
		return false
	}
	receiver, ok := selector.X.(*ast.Ident)
	return ok && names[receiver.Name]
}

func TestPreparedCommandScanRejectsNativeStartBypasses(t *testing.T) {
	source := `package fixture
 import process "github.com/lycaon/lycaon/internal/exec"
 func example(){
 cmd,cleanup,err:=process.PrepareCommand(nil,"fixture",nil,process.ExecOpts{})
 _=cleanup;_=err
 cmd.Start();cmd.Run();cmd.Output();cmd.CombinedOutput();cmd.Wait()
 process.StartPrepared(nil, cmd)
 }`
	f, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, 0)
	contractcheck.FailErr(t, "parse launch scan fixture", err)
	prepared := preparedCommandNames(f, importAliases(f))
	count := 0
	ast.Inspect(f, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok && isRawPreparedStart(call.Fun, prepared) {
			count++
		}
		return true
	})
	if count != 4 {
		t.Fatalf("prepared process bypasses detected=%d, want four", count)
	}
}

// A zero options result inside an explicit non-nil error branch is not a
// launch request. Successful zero returns and populated values still need Launch.
func emptyOptionsErrorReturns(file *ast.File) map[*ast.CompositeLit]bool {
	out := map[*ast.CompositeLit]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		branch, ok := node.(*ast.IfStmt)
		if !ok {
			return true
		}
		condition, ok := branch.Cond.(*ast.BinaryExpr)
		if !ok || condition.Op != token.NEQ {
			return true
		}
		left, leftOK := condition.X.(*ast.Ident)
		right, rightOK := condition.Y.(*ast.Ident)
		if !leftOK || !rightOK || left.Name != "err" || right.Name != "nil" {
			return true
		}
		for _, statement := range branch.Body.List {
			returned, ok := statement.(*ast.ReturnStmt)
			if !ok || len(returned.Results) != 2 {
				continue
			}
			literal, ok := returned.Results[0].(*ast.CompositeLit)
			if !ok || len(literal.Elts) != 0 {
				continue
			}
			err, ok := returned.Results[1].(*ast.Ident)
			if ok && err.Name == "err" {
				out[literal] = true
			}
		}
		return true
	})
	return out
}

func TestLaunchOptionsErrorReturnDoesNotExemptRequests(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", `package exec
func invalid() (ExecOpts, error) { if err := validate(); err != nil { return ExecOpts{}, err }; return ExecOpts{Launch: launch}, nil }
func success() (ExecOpts, error) { return ExecOpts{}, nil }
func unattributed() (ExecOpts, error) { return ExecOpts{Dir: "."}, err }
`, 0)
	contractcheck.FailErr(t, "parse options error-return fixture", err)
	exemptions := emptyOptionsErrorReturns(file)
	var findings int
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if ok && isLaunchOptionsLiteral(literal.Type, "exec", nil) && !hasKey(literal, "Launch") && !exemptions[literal] {
			findings++
		}
		return true
	})
	if len(exemptions) != 1 || findings != 2 {
		t.Fatalf("launch options exemptions=%d findings=%d; want one error result and two refused requests", len(exemptions), findings)
	}
}
