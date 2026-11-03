package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// Child-only launch modes forward the inherited stream without writing logs.
// Recognize only the exact broker arguments and supervisor stream assignment.
func childStdoutForwarding(path string, file *ast.File) map[*ast.SelectorExpr]bool {
	allowed := map[*ast.SelectorExpr]bool{}
	path = filepath.ToSlash(path)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		relay := path == "internal/exec/relay_linux.go" && fn.Name.Name == "runBrokerRelay"
		supervisor := path == "internal/exec/supervisor_linux.go" && fn.Name.Name == "command" && fn.Recv != nil && len(fn.Recv.List) == 1
		if supervisor {
			receiver, ok := fn.Recv.List[0].Type.(*ast.Ident)
			supervisor = ok && receiver.Name == "supervisorTarget"
		}
		if !relay && !supervisor {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok && relay && stdoutSelector(call.Fun, "launchbroker", "Send") && len(call.Args) == 6 && stdoutSelector(call.Args[2], "os", "Stdin") && stdoutSelector(call.Args[3], "os", "Stdout") && stdoutSelector(call.Args[4], "os", "Stderr") {
				allowed[call.Args[3].(*ast.SelectorExpr)] = true
			}
			if assignment, ok := node.(*ast.AssignStmt); ok && supervisor && assignment.Tok == token.ASSIGN && len(assignment.Lhs) == 3 && len(assignment.Rhs) == 3 {
				matches := true
				for i, stream := range []string{"Stdin", "Stdout", "Stderr"} {
					matches = matches && stdoutSelector(assignment.Lhs[i], "cmd", stream) && stdoutSelector(assignment.Rhs[i], "os", stream)
				}
				if matches {
					allowed[assignment.Rhs[1].(*ast.SelectorExpr)] = true
				}
			}
			return true
		})
	}
	return allowed
}

func stdoutSelector(expr ast.Expr, owner, name string) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && pkg.Name == owner && selector.Sel.Name == name
}

func TestChildStdoutForwardingIsExact(t *testing.T) {
	const relay = `func runBrokerRelay() { launchbroker.Send(broker, request, os.Stdin, os.Stdout, os.Stderr, files...) }`
	const supervisor = `func (t supervisorTarget) command() { cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr }`
	for _, tc := range []struct {
		name, path, source string
		allowed, blocked   int
	}{
		{"relay", "internal/exec/relay_linux.go", relay, 1, 0},
		{"supervisor", "internal/exec/supervisor_linux.go", supervisor, 1, 0},
		{"other path", "internal/exec/other.go", relay, 0, 1},
		{"other function", "internal/exec/relay_linux.go", `func serve() { launchbroker.Send(broker, request, os.Stdin, os.Stdout, os.Stderr, files...) }`, 0, 1},
		{"other receiver", "internal/exec/supervisor_linux.go", `func (t other) command() { cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr }`, 0, 1},
		{"wrong broker call", "internal/exec/relay_linux.go", `func runBrokerRelay() { other.Send(broker, request, os.Stdin, os.Stdout, os.Stderr, files...) }`, 0, 1},
		{"wrong stream slot", "internal/exec/relay_linux.go", `func runBrokerRelay() { launchbroker.Send(broker, request, os.Stdout, os.Stdin, os.Stderr, files...) }`, 0, 1},
		{"stdout write", "internal/exec/relay_linux.go", relay + `; func serve() { os.Stdout.Write(data) }`, 1, 1},
		{"write inside relay", "internal/exec/relay_linux.go", `func runBrokerRelay() { launchbroker.Send(broker, request, os.Stdin, os.Stdout, os.Stderr, files...); os.Stdout.Write(data) }`, 1, 1},
		{"supervisor logging", "internal/exec/supervisor_linux.go", supervisor + `; func serve() { fmt.Fprintln(os.Stdout, "bad") }`, 1, 1},
		{"wrong assignment", "internal/exec/supervisor_linux.go", `func (t supervisorTarget) command() { other.Stdin, other.Stdout, other.Stderr = os.Stdin, os.Stdout, os.Stderr }`, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package fixture\n"+tc.source, 0)
			if err != nil {
				t.Fatalf("parse stdout fixture: %v", err)
			}
			allowed := childStdoutForwarding(tc.path, file)
			blocked := 0
			ast.Inspect(file, func(node ast.Node) bool {
				if expr, ok := node.(*ast.SelectorExpr); ok && stdoutSelector(expr, "os", "Stdout") && !allowed[expr] {
					blocked++
				}
				return true
			})
			if len(allowed) != tc.allowed || blocked != tc.blocked {
				t.Fatalf("allowed=%d blocked=%d, want %d/%d", len(allowed), blocked, tc.allowed, tc.blocked)
			}
		})
	}
}
