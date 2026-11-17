package forbiddencalls

import (
	"go/ast"
	"go/token"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// engineStdoutProtocols expose stdout as their output protocol.
var engineStdoutProtocols = []string{
	"internal/startupprotocol",
	"internal/logscli",
	"internal/promptattach/docext",
	"internal/sshproxy",
	"internal/launchguard",
	"internal/layeredfs/adapter/flushhelper",
}

var engineProcess = []laws.Rule{{
	// The shell closes the engine's stdout after startup; a later write can
	// terminate the engine.
	Name:    "engine-serving-path-never-writes-stdout",
	Anchors: []laws.Anchor{laws.Package("internal/startupprotocol"), laws.Func("internal/exec", "runBrokerRelay")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal", "pkg") {
			if laws.Under(f, engineStdoutProtocols...) {
				continue
			}
			forwarded := ChildStdoutForwarding(f.Rel, f.AST)
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				switch {
				case pkg == "os" && name == "Stdout" && !forwarded[sel]:
					r.ReportAt(sel.Pos(), "writes to os.Stdout; engine stdout belongs to the startup protocol")
				case pkg == "github.com/go-chi/chi/v5/middleware" && name == "Logger":
					r.ReportAt(sel.Pos(), "chi middleware.Logger binds os.Stdout; use requestLogger()")
				}
			})
		}
	},
}, {
	// Environment flags are read through configdir.EnvTruthy, one grammar.
	Name:    "env-truthy-has-one-spelling",
	Anchors: []laws.Anchor{laws.Func("internal/configdir", "EnvTruthy")},
	Exempt:  []laws.Exemption{{Key: "internal/gitexec/signing.go:gitConfigTrue", Reason: "git's own config boolean, matched to git"}},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal") {
			if laws.Under(f, "internal/configdir") {
				continue
			}
			f.Funcs(func(fn *ast.FuncDecl) {
				if !returnsOnlyBool(fn) {
					return
				}
				lits := map[string]bool{}
				laws.StringLits(fn.Body, func(_ *ast.BasicLit, value string) { lits[value] = true })
				if lits["1"] && lits["true"] && !r.Exempt(f.Rel+":"+fn.Name.Name) {
					r.ReportAt(fn.Pos(), "%s parses an environment flag; call configdir.EnvTruthy", fn.Name.Name)
				}
			})
		}
	},
}}

func returnsOnlyBool(fn *ast.FuncDecl) bool {
	res := fn.Type.Results
	if res == nil || len(res.List) != 1 || len(res.List[0].Names) > 1 {
		return false
	}
	ident, ok := res.List[0].Type.(*ast.Ident)
	return ok && ident.Name == "bool"
}

// ChildStdoutForwarding returns the os.Stdout selectors that hand the
// inherited stream to a child: the broker relay's exact Send arguments and
// the supervisor's exact stream assignment.
func ChildStdoutForwarding(path string, file *ast.File) map[*ast.SelectorExpr]bool {
	allowed := map[*ast.SelectorExpr]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
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
			if call, ok := node.(*ast.CallExpr); ok && relay && StdoutSelector(call.Fun, "launchbroker", "Send") && len(call.Args) == 6 &&
				StdoutSelector(call.Args[2], "os", "Stdin") && StdoutSelector(call.Args[3], "os", "Stdout") && StdoutSelector(call.Args[4], "os", "Stderr") {
				allowed[call.Args[3].(*ast.SelectorExpr)] = true
			}
			if assignment, ok := node.(*ast.AssignStmt); ok && supervisor && assignment.Tok == token.ASSIGN && len(assignment.Lhs) == 3 && len(assignment.Rhs) == 3 {
				matches := true
				for i, stream := range []string{"Stdin", "Stdout", "Stderr"} {
					matches = matches && StdoutSelector(assignment.Lhs[i], "cmd", stream) && StdoutSelector(assignment.Rhs[i], "os", stream)
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

// StdoutSelector reports owner.name on an identifier.
func StdoutSelector(expr ast.Expr, owner, name string) bool {
	return laws.SelectorName(expr) == owner+"."+name
}
