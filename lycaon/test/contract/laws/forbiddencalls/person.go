package forbiddencalls

import (
	"go/ast"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

var person = []laws.Rule{{
	// Only admitted authorship falls back to the host's person; a decision
	// uses people.Deciding and refuses without a caller.
	Name:    "acting-fallback-only-for-admitted-authorship",
	Anchors: []laws.Anchor{laws.Func("internal/people", "Acting"), laws.Func("internal/people", "Deciding")},
	Exempt: []laws.Exemption{
		{Key: "internal/session/store:sqlSessions.CreateWithStatus", Reason: "a root chat the host opens belongs to the host's person"},
		{Key: "internal/session/store:memorySessions.CreateWithStatus", Reason: "memory twin of the SQL session store"},
		{Key: "internal/session/transcript:Service.PromptAuthor", Reason: "a prompt without an admission receipt is authored by its request's caller"},
		{Key: "internal/session/submissions:Service.admitPrompt", Reason: "a user prompt admission names its request's caller"},
		{Key: "internal/editordoc:authorshipTracker.personActor", Reason: "editor transitions are authored by their client's person"},
		{Key: "internal/sourceledger:Store.operationPerson", Reason: "user-origin source operations without an explicit person"},
		{Key: "internal/projectsource:SourceMutationJournal.insertWith", Reason: "user file mutations journal their person"},
	},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, laws.Source...) {
			if laws.Under(f, "internal/people") {
				continue
			}
			f.Funcs(func(fn *ast.FuncDecl) {
				calls := false
				f.Calls(fn.Body, func(_ *ast.CallExpr, pkg, name string) {
					calls = calls || pkg == "github.com/lycaon/lycaon/internal/people" && name == "Acting"
				})
				if key := f.Dir + ":" + laws.FuncName(fn); calls && !r.Exempt(key) {
					r.Report("%s: people.Acting credits the host's person outside admitted authorship", key)
				}
			})
		}
	},
}, {
	// State-changing routes register through registerV1Operation, which
	// records the person action.
	Name: "state-changing-routes-record-the-person-action",
	Anchors: []laws.Anchor{
		laws.Func("internal/api", "Server.registerV1Operation"),
		laws.Func("internal/api", "registerRootOperation"),
	},
	Exempt: []laws.Exemption{
		{Key: "internal/api/harness_control.go", Reason: "harness control plane for driven test stacks"},
		{Key: "internal/api/harness_preparation.go", Reason: "harness control plane for driven test stacks"},
	},
	Check: func(r *laws.Run) {
		if !callsMethod(r.FuncDecl("internal/api", "Server.registerV1Operation").Body, "recordPersonAction") {
			r.Report("registerV1Operation must wrap every handler with recordPersonAction")
		}
		for _, f := range r.GoFiles(laws.Production, "internal/api") {
			if f.Dir != "internal/api" {
				continue
			}
			f.Funcs(func(fn *ast.FuncDecl) {
				if fn.Name.Name == "registerV1Operation" || fn.Name.Name == "registerRootOperation" ||
					!registersStateChangingRoute(fn.Body) || r.Exempt(f.Rel) {
					return
				}
				r.Report("%s:%s registers a state-changing route outside registerV1Operation", f.Rel, fn.Name.Name)
			})
		}
	},
}, {
	// Unauthenticated root operations bypass caller binding, so they read.
	Name:    "root-operations-are-reads",
	Anchors: []laws.Anchor{laws.Func("internal/api", "registerRootOperation"), laws.Decl("internal/api", "generatedOperation.Method")},
	Check: func(r *laws.Run) {
		files := r.GoFiles(laws.Production, "internal/api")
		methods := map[string]string{}
		for _, f := range files {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				value, ok := n.(*ast.ValueSpec)
				if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
					return true
				}
				lit, ok := value.Values[0].(*ast.CompositeLit)
				if !ok || laws.TypeName(lit.Type) != "generatedOperation" {
					return true
				}
				for _, elt := range lit.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Method" {
							methods[value.Names[0].Name], _ = laws.StringLit(kv.Value)
						}
					}
				}
				return true
			})
		}
		r.Expect(len(methods), "generated operations")
		for _, f := range files {
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				if laws.CalleeName(call) != "registerRootOperation" || len(call.Args) < 2 {
					return
				}
				op, ok := call.Args[1].(*ast.Ident)
				if !ok {
					r.ReportAt(call.Pos(), "registerRootOperation takes a generated operation variable")
				} else if methods[op.Name] != "GET" {
					r.ReportAt(call.Pos(), "%s is %q; root operations must be reads", op.Name, methods[op.Name])
				}
			})
		}
	},
}}

// callsMethod reports a call to any receiver's method name below body.
func callsMethod(body ast.Node, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
				found = true
			}
		}
		return !found
	})
	return found
}

// registersStateChangingRoute finds a router registration of a literal
// route with a state-changing method, or a non-GET Method registration.
func registersStateChangingRoute(body ast.Node) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || found {
			return !found
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		first, _ := laws.StringLit(call.Args[0])
		switch sel.Sel.Name {
		case "Post", "Put", "Patch", "Delete":
			found = strings.HasPrefix(first, "/")
		case "Method", "MethodFunc":
			found = !strings.EqualFold(first, "GET")
		}
		return !found
	})
	return found
}
