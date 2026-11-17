package forbiddencalls

import (
	"fmt"
	"go/ast"
	"go/token"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// runVarsHome is the vars primitive's own file.
const runVarsHome = "internal/workflow/runstate/variables.go"

// runVarsCallerHoldsLock participates in a wider caller-managed critical section.
const runVarsCallerHoldsLock = "stampLocked"

var runVars = []laws.Rule{{
	// Scaffold vars are written only by the primitive, which loads the run
	// under its lock immediately before the compare-and-set.
	Name:    "run-vars-written-by-the-primitive",
	Anchors: []laws.Anchor{laws.GoPath(runVarsHome), laws.Func("internal/workflow/runstate", "Variables."+runVarsCallerHoldsLock)},
	Exempt: []laws.Exemption{{
		Key:    "internal/workflow/approval_service.go",
		Reason: "CommitBlueprintApproval commits vars and the approval record together; its writers keep the fresh-load shape",
	}},
	Check: func(r *laws.Run) {
		home := false
		for _, f := range r.GoFiles(laws.Production, "internal") {
			writers := runVarsWritingFuncs(f.AST)
			if len(writers) > 0 && r.Exempt(f.Rel) {
				for _, offence := range runVarsFreshLoadOffences(f.Rel, writers) {
					r.Report("%s", offence)
				}
				continue
			}
			if f.Rel == runVarsHome {
				home = len(writers) > 0
				for _, offence := range runVarsFreshLoadOffences(f.Rel, writers) {
					r.Report("%s", offence)
				}
				continue
			}
			for _, w := range writers {
				r.ReportAt(w.decl.Pos(), "%s writes vars outside %s", w.name, runVarsHome)
			}
		}
		if !home {
			r.Report("%s commits no vars", runVarsHome)
		}
	},
}, {
	// A writer that accepts a run could commit against a stale revision; it
	// takes a RunVarsMutation instead.
	Name:    "run-vars-primitive-takes-no-run",
	Anchors: []laws.Anchor{laws.GoPath(runVarsHome)},
	Check: func(r *laws.Run) {
		f := r.GoFile(runVarsHome)
		f.Funcs(func(fn *ast.FuncDecl) {
			for _, param := range fn.Type.Params.List {
				if star, ok := param.Type.(*ast.StarExpr); ok && laws.TypeName(star.X) == "WorkflowRun" {
					r.ReportAt(param.Pos(), "%s accepts a caller-supplied run", fn.Name.Name)
				}
			}
		})
	},
}}

type runVarsWriter struct {
	name      string
	decl      *ast.FuncDecl
	writePos  token.Pos
	loadPos   token.Pos
	lockPos   token.Pos
	hasLoad   bool
	hasLock   bool
	readsVars bool
}

// runVarsWritingFuncs returns each function that commits the vars row, with the
// positions of its lock, its run load, and its write.
func runVarsWritingFuncs(file *ast.File) []runVarsWriter {
	var out []runVarsWriter
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		w := runVarsWriter{name: fn.Name.Name, decl: fn}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "UpdateVars", "CommitBlueprintApproval":
				if !w.writePos.IsValid() {
					w.writePos = sel.Pos()
				}
			case "loadRun", "ActiveBySession", "Get":
				if !w.hasLoad {
					w.hasLoad, w.loadPos = true, sel.Pos()
				}
			case "Lock":
				if !w.hasLock {
					w.hasLock, w.lockPos = true, sel.Pos()
				}
			case "GetScaffoldVars":
				w.readsVars = true
			}
			return true
		})
		if w.writePos.IsValid() {
			out = append(out, w)
		}
	}
	return out
}

// runVarsFreshLoadOffences reports a writer whose CAS could carry a revision
// read before its critical section began.
func runVarsFreshLoadOffences(rel string, writers []runVarsWriter) []string {
	var out []string
	for _, w := range writers {
		if !w.hasLoad {
			out = append(out, fmt.Sprintf("%s: %s commits vars without loading the run in the same function",
				rel, w.name))
			continue
		}
		if w.loadPos > w.writePos {
			out = append(out, fmt.Sprintf("%s: %s loads the run after committing vars", rel, w.name))
			continue
		}
		if w.name == runVarsCallerHoldsLock {
			// The caller holds the lock; this function loads the run.
			continue
		}
		if !w.hasLock {
			out = append(out, fmt.Sprintf("%s: %s commits vars without lockRunVars", rel, w.name))
			continue
		}
		if w.lockPos > w.loadPos {
			out = append(out, fmt.Sprintf("%s: %s loads the run before taking lockRunVars — "+
				"the compare-and-set would carry a pre-lock revision", rel, w.name))
		}
	}
	return out
}
