package forbiddencalls

import (
	"go/ast"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

var workerViews = []laws.Rule{{
	// A worker view is served inside the engine, so a child entering one
	// before exec can hang the engine under GC. Processes start through
	// internal/exec, whose helper enters the directory after exec.
	Name:    "worker-view-processes-enter-through-the-launch-helper",
	Anchors: []laws.Anchor{laws.Decl("internal/exec", "ExecOpts.Dir")},
	Exempt: []laws.Exemption{
		{Key: "internal/exec/pipeline.go", Reason: "records the requested directory on a scratch command that is never started; the launch pins it for the helper"},
		{Key: "internal/confine/process_pin_binding.go", Reason: "clears Dir on the helper command, which is what makes entry happen after exec"},
		{Key: "internal/confine/process_attributes_unix.go", Reason: "starts a limit probe at the filesystem root, which is never a view"},
	},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal") {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				if setsCommandDir(n) && !r.Exempt(f.Rel) {
					r.ReportAt(n.Pos(), "sets a raw working directory; start through internal/exec with ExecOpts.Dir")
				}
				return true
			})
		}
	},
}, {
	// View path consumers flush through internal/fssync and map nothing: the
	// server runs in this process, so a blocking flush or page fault there
	// can stop the engine.
	Name:    "view-path-consumers-flush-through-fssync",
	Anchors: []laws.Anchor{laws.Package("internal/fssync")},
	Check: func(r *laws.Run) {
		dirs := []string{"internal/workspace", "internal/worker", "internal/session", "internal/tools", "internal/toolhost",
			"internal/layeredfs/consumer", "internal/api/sourceapi"}
		for _, f := range r.GoFiles(laws.Production, dirs...) {
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				if name := laws.CalleeName(call); name == "Sync" && len(call.Args) == 0 || name == "Mmap" {
					r.ReportAt(call.Pos(), "%s on a view path consumer; flush through internal/fssync and map nothing", name)
				}
			})
		}
	},
}}

// setsCommandDir recognizes `<cmd>.Dir = ...`, including tuple assignments, and `exec.Cmd{Dir: ...}`.
func setsCommandDir(n ast.Node) bool {
	switch node := n.(type) {
	case *ast.AssignStmt:
		for _, lhs := range node.Lhs {
			if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Dir" && commandLikeName(sel.X) {
				return true
			}
		}
	case *ast.CompositeLit:
		sel, ok := node.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Cmd" {
			return false
		}
		for _, elt := range node.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Dir" {
					return true
				}
			}
		}
	}
	return false
}

// commandLikeName limits the check to os/exec commands by the receiver's name, which is the only
// fact a syntax scan has; other types with a Dir field use other names in this tree.
func commandLikeName(x ast.Expr) bool {
	id, ok := x.(*ast.Ident)
	if !ok {
		return false
	}
	name := strings.ToLower(id.Name)
	return name == "c" || strings.Contains(name, "cmd") || strings.Contains(name, "command") || strings.Contains(name, "proc") || name == "final"
}
