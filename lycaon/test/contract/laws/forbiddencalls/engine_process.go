package forbiddencalls

import (
	"go/ast"
	"go/build/constraint"
	"go/token"
	"strings"

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

const gotreesitterPkg = "github.com/odvcencio/gotreesitter"

// parsingTaggerMethods parse internally and bypass the budget like a raw
// parser. TagTree tags a tree the caller already parsed, so it is absent.
var parsingTaggerMethods = []string{"Tag", "TagUTF16", "TagUTF16Bytes", "TagIncremental", "TagIncrementalUTF16", "TagIncrementalUTF16Bytes"}

// timeoutClaims are functions whose callers rely on their own internal
// deadline, as their contextcheck exemptions say.
var timeoutClaims = []struct{ dir, name string }{
	{"internal/project", "gitRemoteHash"},
	{"internal/worker", "sqlQueueCore.Get"},
	{"internal/llm", "fetchDiscoveredModels"},
}

var hostBounds = []laws.Rule{{
	// Tree-sitter parses go through internal/tsparse, which applies the budget.
	Name:    "parses-go-through-the-bounded-seam",
	Anchors: []laws.Anchor{laws.Func("internal/tsparse", "Parse")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, laws.Source...) {
			if laws.Under(f, "internal/tsparse") || !f.Imports(gotreesitterPkg) {
				continue
			}
			f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				switch {
				case pkg == gotreesitterPkg && name == "NewParser":
					r.ReportAt(call.Pos(), "gotreesitter.NewParser is unbounded; use tsparse.Parse")
				case ok && contains(parsingTaggerMethods, sel.Sel.Name):
					r.ReportAt(call.Pos(), "Tagger.%s parses unbounded; parse with tsparse.Parse and use TagTree", sel.Sel.Name)
				}
			})
		}
	},
}, {
	// A function its callers trust to bound itself constructs its deadline.
	Name:    "bounded-timeout-claims-construct-a-deadline",
	Anchors: timeoutClaimAnchors(),
	Check: func(r *laws.Run) {
		for _, claim := range timeoutClaims {
			fn := r.FuncDecl(claim.dir, claim.name)
			f := r.Declaration(claim.dir, claim.name).File
			bounded := false
			f.Calls(fn.Body, func(_ *ast.CallExpr, pkg, name string) {
				bounded = bounded || pkg == "context" && (name == "WithTimeout" || name == "WithDeadline")
			})
			if !bounded {
				r.ReportAt(fn.Pos(), "%s claims an internal timeout but constructs no deadline", claim.name)
			}
		}
	},
}, {
	// Chrome stays in its launcher's process group, so a hard-killed parent's
	// group sweep reaches it.
	Name:    "browser-keeps-its-launcher-process-group",
	Anchors: []laws.Anchor{laws.Package("internal/browser"), laws.Package("internal/browserengine")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.AnyGo, "internal/browser", "internal/browserengine") {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && contains([]string{"Setpgid", "Pdeathsig", "CREATE_NEW_PROCESS_GROUP"}, sel.Sel.Name) {
					r.ReportAt(sel.Pos(), "sets %s; chrome stays in its launcher's group", sel.Sel.Name)
				}
				return true
			})
		}
	},
}, {
	// The browser resolves from the environment, the bundle or the managed
	// cache and refuses otherwise; it never searches PATH.
	Name:    "browser-never-resolves-from-path",
	Anchors: []laws.Anchor{laws.Package("internal/browser"), laws.Package("internal/browserengine")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.AnyGo, "internal/browser", "internal/browserengine") {
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				if strings.HasSuffix(laws.CalleeName(call), "LookPath") {
					r.ReportAt(call.Pos(), "%s searches PATH", laws.CalleeName(call))
				}
			})
		}
	},
}}

func timeoutClaimAnchors() []laws.Anchor {
	out := make([]laws.Anchor, 0, len(timeoutClaims))
	for _, claim := range timeoutClaims {
		out = append(out, laws.Func(claim.dir, claim.name))
	}
	return out
}

var confineHelper = []laws.Rule{{
	// The confinement helper dispatches from init in every build but the
	// helper's own coverage main, is not exported, and refuses nesting.
	Name:    "helper-dispatch-is-confine-init",
	Anchors: []laws.Anchor{laws.Func("internal/confine", "runHelperIfInvoked"), laws.Func("internal/confine", "Command")},
	Check: func(r *laws.Run) {
		initDispatches := false
		for _, f := range r.GoFiles(laws.Production, "internal/confine") {
			if f.Dir != "internal/confine" || onlyInHelperMainBuild(f.AST) {
				continue
			}
			f.Funcs(func(fn *ast.FuncDecl) {
				switch {
				case fn.Name.Name == "RunHelperIfInvoked":
					r.ReportAt(fn.Pos(), "RunHelperIfInvoked is exported; dispatch stays inside confine's init")
				case fn.Name.Name == "init" && fn.Recv == nil && mentions(fn.Body, "runHelperIfInvoked"):
					initDispatches = true
				}
			})
		}
		if !initDispatches {
			r.Report("no init in internal/confine calls runHelperIfInvoked")
		}
		command := r.FuncDecl("internal/confine", "Command")
		if !mentions(command.Body, "IsHelperInvocation") || !mentions(command.Body, "ErrNestedHelper") {
			r.ReportAt(command.Pos(), "Command must refuse IsHelperInvocation with ErrNestedHelper")
		}
	},
}, {
	// Automatic confinement is a production opt-in; tests confine explicitly.
	Name:    "enable-auto-confine-is-production-opt-in",
	Anchors: []laws.Anchor{laws.Func("internal/confine", "EnableAutoConfine")},
	Check: func(r *laws.Run) {
		production := 0
		for _, f := range r.GoFiles(laws.AnyGo, "internal", "cmd", "pkg", "config", "test") {
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				if laws.CalleeName(call) != "EnableAutoConfine" {
					return
				}
				if f.IsTest {
					r.ReportAt(call.Pos(), "a test enables automatic confinement")
				} else {
					production++
				}
			})
		}
		r.Expect(production, "production EnableAutoConfine calls")
	},
}}

// onlyInHelperMainBuild reports a file compiled only under confinehelpermain,
// the helper's coverage build, which dispatches from its own main.
func onlyInHelperMainBuild(f *ast.File) bool {
	for _, group := range f.Comments {
		if group.Pos() >= f.Package {
			break
		}
		for _, c := range group.List {
			if expr, err := constraint.Parse(c.Text); err == nil {
				return requiresTag(expr, "confinehelpermain")
			}
		}
	}
	return false
}

func requiresTag(e constraint.Expr, tag string) bool {
	switch x := e.(type) {
	case *constraint.TagExpr:
		return x.Tag == tag
	case *constraint.AndExpr:
		return requiresTag(x.X, tag) || requiresTag(x.Y, tag)
	}
	return false
}

// mentions reports an identifier named name below node.
func mentions(node ast.Node, name string) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		found = found || ok && id.Name == name
		return !found
	})
	return found
}
