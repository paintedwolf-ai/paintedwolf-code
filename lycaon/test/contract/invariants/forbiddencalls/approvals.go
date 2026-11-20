package forbiddencalls

import (
	"go/ast"
	"go/types"
	"strings"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

var approvals = []invariants.Invariant{{
	// The approval face is computed only by plan construction, refacing and
	// the checkpoint wire projection.
	Name:    "recommended-option-set-in-plan-construction",
	Anchors: []invariants.Anchor{invariants.Decl("internal/hitl", "ApprovalPlan.RecommendedOptionID")},
	Exempt: []invariants.Exemption{
		{Key: "internal/hitl/approval_plan.go", Reason: "plan construction"},
		{Key: "internal/hitl/checkpoint_wire.go", Reason: "checkpoint wire projection"},
		{Key: "internal/hitl/approval_offers.go:refaceAfterFilter", Reason: "refacing after the option filter"},
	},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Production, "internal") {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				var at ast.Node
				switch node := n.(type) {
				case *ast.KeyValueExpr:
					if key, ok := node.Key.(*ast.Ident); ok && key.Name == "RecommendedOptionID" {
						at = node
					}
				case *ast.AssignStmt:
					for _, lhs := range node.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "RecommendedOptionID" {
							at = node
						}
					}
				}
				if at != nil && !r.Exempt(f.Rel) && !r.Exempt(f.Site(at.Pos())) {
					r.ReportAt(at.Pos(), "writes RecommendedOptionID outside approval plan construction")
				}
				return true
			})
		}
	},
}, {
	// A posture reaches host code only through gate.ParsePosture or decoding,
	// both of which refuse unknown tokens.
	Name:    "approval-postures-come-from-the-parser",
	Anchors: []invariants.Anchor{invariants.Decl("internal/gate", "Posture"), invariants.Func("internal/gate", "ParsePosture")},
	Check: func(r *invariants.Run) {
		const gate = "github.com/lycaon/lycaon/internal/gate"
		for _, f := range r.GoFiles(invariants.Production, invariants.Source...) {
			f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
				if pkg == gate && name == "Posture" && len(call.Args) == 1 {
					r.ReportAt(call.Pos(), "gate.Posture(...) conversion carries an unchecked token; use gate.ParsePosture")
				}
			})
		}
	},
}}

var concurrency = []invariants.Invariant{{
	// Admission.deps is written by setDeps during a prompt turn while host
	// wake paths read it, so reads go through loopDeps under depsMu.
	Name: "loop-admission-deps-read-through-accessor",
	Anchors: []invariants.Anchor{
		invariants.Decl("internal/coordinator/loopwake", "Admission.deps"),
		invariants.Func("internal/coordinator/loopwake", "Admission.loopDeps"),
		invariants.Func("internal/coordinator/loopwake", "Admission.setDeps"),
	},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Production, "internal/coordinator/loopwake") {
			f.Funcs(func(fn *ast.FuncDecl) {
				if invariants.ReceiverType(fn) != "Admission" || fn.Name.Name == "loopDeps" || fn.Name.Name == "setDeps" ||
					len(fn.Recv.List[0].Names) == 0 {
					return
				}
				recv := fn.Recv.List[0].Names[0].Name
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "deps" {
						if id, ok := sel.X.(*ast.Ident); ok && id.Name == recv {
							r.ReportAt(sel.Pos(), "%s reads %s.deps directly; snapshot through %s.loopDeps()", fn.Name.Name, recv, recv)
						}
					}
					return true
				})
			})
		}
	},
}}

var isolation = []invariants.Invariant{{
	// Isolation codes come from the outcome registry's constants.
	Name:    "isolation-codes-have-one-registry",
	Anchors: []invariants.Anchor{invariants.GoPath("internal/isolation/outcomes.go")},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Production, "internal") {
			if f.Rel == "internal/isolation/outcomes.go" {
				continue
			}
			invariants.StringLits(f.AST, func(lit *ast.BasicLit, value string) {
				if strings.HasPrefix(value, "SANDBOX_") || strings.HasPrefix(value, "REMOTE_PACKAGE_EXECUTION_") {
					r.ReportAt(lit.Pos(), "isolation code %s outside internal/isolation/outcomes.go", value)
				}
			})
		}
	},
}, {
	// Approval verdicts stay silent handling or a human ask; the control
	// plane and human rules are the only hard denies.
	Name:    "approval-plane-has-no-system-deny",
	Anchors: []invariants.Anchor{invariants.Decl("internal/gate", "Verdict"), invariants.Decl("internal/hitl", "ApprovalResult.Denied")},
	Exempt: []invariants.Exemption{
		{Key: "internal/settings/rule_gate.go:deniedByRules", Reason: "a human policy rule denies"},
		{Key: "internal/settings/rule_gate.go:controlPlaneDenyResult", Reason: "the control plane is the one path-shaped hard deny"},
	},
	Check: func(r *invariants.Run) {
		gate := r.TypedPackage("internal/gate").Types
		var verdicts []string
		for _, name := range gate.Scope().Names() {
			if c, ok := gate.Scope().Lookup(name).(*types.Const); ok {
				if named, ok := c.Type().(*types.Named); ok && named.Obj().Name() == "Verdict" && named.Obj().Pkg() == gate {
					verdicts = append(verdicts, name)
				}
			}
		}
		r.Diff("gate.Verdict constants", []string{"Ask", "Silent"}, verdicts)
		minted := map[string]int{}
		for _, f := range r.GoFiles(invariants.Production, "internal") {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || invariants.TypeName(lit.Type) != "ApprovalResult" || !trueField(lit, "Denied") {
					return true
				}
				site := f.Site(lit.Pos())
				minted[site]++
				if !r.Exempt(site) {
					r.ReportAt(lit.Pos(), "ApprovalResult{Denied: true} outside human policy and control-plane enforcement")
				}
				return true
			})
		}
		for site, n := range minted {
			if n > 1 {
				r.Report("%s mints %d hard denies, want one", site, n)
			}
		}
	},
}}

// trueField reports a composite literal setting field to true.
func trueField(lit *ast.CompositeLit, field string) bool {
	for _, elt := range lit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			if key, ok := kv.Key.(*ast.Ident); ok && key.Name == field {
				value, ok := kv.Value.(*ast.Ident)
				return ok && value.Name == "true"
			}
		}
	}
	return false
}

const bootRecoveryPkg = "github.com/lycaon/lycaon/internal/bootrecovery"

var bootRecovery = []invariants.Invariant{{
	// Startup recovery registers a bootrecovery.Entry: ordering and failure
	// policy belong to the registry.
	Name:    "startup-recovery-runs-through-the-registry",
	Anchors: []invariants.Anchor{invariants.Decl("internal/bootrecovery", "Entry")},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Production, "internal/app") {
			var stack []ast.Node
			ast.Inspect(f.AST, func(n ast.Node) bool {
				if n == nil {
					stack = stack[:len(stack)-1]
					return true
				}
				if call, ok := n.(*ast.CallExpr); ok {
					name := invariants.CalleeName(call)
					if (strings.HasPrefix(name, "Recover") || strings.HasPrefix(name, "runRecover")) &&
						!insideEntry(f, stack) {
						r.ReportAt(call.Pos(), "calls %s inline; register a bootrecovery.Entry", name)
					}
				}
				stack = append(stack, n)
				return true
			})
		}
	},
}, {
	// An entry that omits a field cannot be registered for startup.
	Name:    "recovery-entries-declare-kind-and-phase",
	Anchors: []invariants.Anchor{invariants.Decl("internal/bootrecovery", "Entry.Kind"), invariants.Decl("internal/bootrecovery", "Entry.Phase")},
	Check: func(r *invariants.Run) {
		entries := 0
		for _, f := range r.GoFiles(invariants.Production, "internal/app") {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || !isRecoveryEntry(f, lit) {
					return true
				}
				entries++
				for _, field := range []string{"Name", "Kind", "Phase", "Run"} {
					if !setsField(lit, field) {
						r.ReportAt(lit.Pos(), "bootrecovery.Entry omits %s", field)
					}
				}
				return true
			})
		}
		r.Expect(entries, "bootrecovery.Entry literals in internal/app")
	},
}}

func isRecoveryEntry(f *invariants.GoFile, lit *ast.CompositeLit) bool {
	pkg, name, ok := f.Qualified(lit.Type)
	return ok && pkg == bootRecoveryPkg && name == "Entry"
}

func insideEntry(f *invariants.GoFile, stack []ast.Node) bool {
	for _, n := range stack {
		if lit, ok := n.(*ast.CompositeLit); ok && isRecoveryEntry(f, lit) {
			return true
		}
	}
	return false
}

var person = []invariants.Invariant{{
	// Only admitted authorship falls back to the host's person; a decision
	// uses people.Deciding and refuses without a caller.
	Name:    "acting-fallback-only-for-admitted-authorship",
	Anchors: []invariants.Anchor{invariants.Func("internal/people", "Acting"), invariants.Func("internal/people", "Deciding")},
	Exempt: []invariants.Exemption{
		{Key: "internal/session/store:sqlSessions.CreateWithStatus", Reason: "a root chat the host opens belongs to the host's person"},
		{Key: "internal/session/store:memorySessions.CreateWithStatus", Reason: "memory twin of the SQL session store"},
		{Key: "internal/session/transcript:Service.PromptAuthor", Reason: "a prompt without an admission receipt is authored by its request's caller"},
		{Key: "internal/session/submissions:Service.admitPrompt", Reason: "a user prompt admission names its request's caller"},
		{Key: "internal/editordoc:authorshipTracker.personActor", Reason: "editor transitions are authored by their client's person"},
		{Key: "internal/sourceledger:Store.operationPerson", Reason: "user-origin source operations without an explicit person"},
		{Key: "internal/projectsource:SourceMutationJournal.insertWith", Reason: "user file mutations journal their person"},
	},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Production, invariants.Source...) {
			if invariants.Under(f, "internal/people") {
				continue
			}
			f.Funcs(func(fn *ast.FuncDecl) {
				calls := false
				f.Calls(fn.Body, func(_ *ast.CallExpr, pkg, name string) {
					calls = calls || pkg == "github.com/lycaon/lycaon/internal/people" && name == "Acting"
				})
				if key := f.Dir + ":" + invariants.FuncName(fn); calls && !r.Exempt(key) {
					r.Report("%s: people.Acting credits the host's person outside admitted authorship", key)
				}
			})
		}
	},
}, {
	// State-changing routes register through registerV1Operation, which
	// records the person action.
	Name: "state-changing-routes-record-the-person-action",
	Anchors: []invariants.Anchor{
		invariants.Func("internal/api", "Server.registerV1Operation"),
		invariants.Func("internal/api", "registerRootOperation"),
	},
	Exempt: []invariants.Exemption{
		{Key: "internal/api/harness_control.go", Reason: "harness control plane for driven test stacks"},
		{Key: "internal/api/harness_preparation.go", Reason: "harness control plane for driven test stacks"},
	},
	Check: func(r *invariants.Run) {
		if !callsMethod(r.FuncDecl("internal/api", "Server.registerV1Operation").Body, "recordPersonAction") {
			r.Report("registerV1Operation must wrap every handler with recordPersonAction")
		}
		for _, f := range r.GoFiles(invariants.Production, "internal/api") {
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
	Anchors: []invariants.Anchor{invariants.Func("internal/api", "registerRootOperation"), invariants.Decl("internal/api", "generatedOperation.Method")},
	Check: func(r *invariants.Run) {
		files := r.GoFiles(invariants.Production, "internal/api")
		methods := map[string]string{}
		for _, f := range files {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				value, ok := n.(*ast.ValueSpec)
				if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
					return true
				}
				lit, ok := value.Values[0].(*ast.CompositeLit)
				if !ok || invariants.TypeName(lit.Type) != "generatedOperation" {
					return true
				}
				for _, elt := range lit.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Method" {
							methods[value.Names[0].Name], _ = invariants.StringLit(kv.Value)
						}
					}
				}
				return true
			})
		}
		r.Expect(len(methods), "generated operations")
		for _, f := range files {
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				if invariants.CalleeName(call) != "registerRootOperation" || len(call.Args) < 2 {
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
		first, _ := invariants.StringLit(call.Args[0])
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
