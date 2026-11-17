package forbiddencalls

import (
	"go/ast"
	"go/token"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

var processLifetime = []laws.Rule{{
	// A collector may abandon a stream on its error path, so every hand-off
	// send in the streaming path selects on ctx.Done(); a bare send strands
	// the producer and holds the response body with it.
	Name:    "stream-producer-sends-stay-cancelable",
	Anchors: []laws.Anchor{laws.Decl("internal/llm/modelcall", "StreamChunk")},
	Check: func(r *laws.Run) {
		channels := 0
		for _, f := range r.GoFiles(laws.Production, "internal/llm") {
			channels += len(streamChunkChannels(f.AST))
			for _, send := range bareStreamSends(f.AST) {
				r.ReportAt(send.Pos(), "bare send on a stream-chunk channel; select on ctx.Done()")
			}
		}
		r.Expect(channels, "stream-chunk channels in internal/llm")
	},
}, {
	// Program lookups use the engine's resolved PATH: a GUI-launched engine
	// inherits a minimal process PATH that misses tools every child can run.
	Name:    "program-lookups-use-the-resolved-path",
	Anchors: []laws.Anchor{laws.Func("internal/exec", "LookPath"), laws.Func("internal/exec", "EffectivePathValue")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal") {
			if laws.Under(f, "internal/exec", "internal/confine") {
				continue
			}
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				if pkg == "os/exec" && name == "LookPath" {
					r.ReportAt(sel.Pos(), "os/exec.LookPath searches the process PATH; use internal/exec")
				}
			})
			f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
				if pkg == "os" && (name == "Getenv" || name == "LookupEnv") && len(call.Args) > 0 {
					if value, _ := laws.StringLit(call.Args[0]); value == "PATH" {
						r.ReportAt(call.Pos(), "os.%s(\"PATH\") reads the process PATH; use exec.EffectivePathValue", name)
					}
				}
			})
		}
	},
}}

// bareStreamSends returns sends on a stream-chunk channel outside a select,
// in functions that do not make their own buffered stream.
func bareStreamSends(f *ast.File) []*ast.SendStmt {
	channels := streamChunkChannels(f)
	var out []*ast.SendStmt
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || buffersItsOwnStream(fn) {
			continue
		}
		var selects []*ast.SelectStmt
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.SelectStmt:
				selects = append(selects, node)
			case *ast.SendStmt:
				ident, ok := node.Chan.(*ast.Ident)
				if !ok || !channels[ident.Name] {
					return true
				}
				for _, sel := range selects {
					if sel.Pos() <= node.Pos() && node.End() <= sel.End() {
						return true
					}
				}
				out = append(out, node)
			}
			return true
		})
	}
	return out
}

// buffersItsOwnStream reports whether fn creates its stream channel with a
// positive buffer, which is the precondition a "cannot block" declaration rests
// on.
func buffersItsOwnStream(fn *ast.FuncDecl) bool {
	buffered := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		fnIdent, ok := call.Fun.(*ast.Ident)
		if !ok || fnIdent.Name != "make" {
			return true
		}
		ch, ok := call.Args[0].(*ast.ChanType)
		if !ok {
			return true
		}
		if laws.TypeName(ch.Value) != "StreamChunk" {
			return true
		}
		lit, ok := call.Args[1].(*ast.BasicLit)
		if ok && lit.Kind == token.INT && lit.Value != "0" {
			buffered = true
		}
		return true
	})
	return buffered
}

// streamChunkChannels names the identifiers in a file that carry StreamChunk:
// channels made with make(chan StreamChunk, …) and parameters declared as one.
func streamChunkChannels(f *ast.File) map[string]bool {
	names := map[string]bool{}

	isStreamChunkChan := func(expr ast.Expr) bool {
		ch, ok := expr.(*ast.ChanType)
		if !ok {
			return false
		}
		return laws.TypeName(ch.Value) == "StreamChunk"
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for i, rhs := range node.Rhs {
				call, ok := rhs.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					continue
				}
				fnIdent, ok := call.Fun.(*ast.Ident)
				if !ok || fnIdent.Name != "make" || !isStreamChunkChan(call.Args[0]) {
					continue
				}
				if i < len(node.Lhs) {
					if lhs, ok := node.Lhs[i].(*ast.Ident); ok {
						names[lhs.Name] = true
					}
				}
			}
		case *ast.FuncType:
			if node.Params == nil {
				return true
			}
			for _, param := range node.Params.List {
				if !isStreamChunkChan(param.Type) {
					continue
				}
				for _, name := range param.Names {
					names[name.Name] = true
				}
			}
		}
		return true
	})
	return names
}

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
