package forbiddencalls

import (
	"go/ast"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

var renderEntries = []struct{ dir, function string }{
	{"internal/guidance", "StaticRejectFormatter.Format"},
	{"internal/guidance", "EnvelopeHintMessage"},
	{"internal/parse", "DefaultService.Validate"},
	{"internal/hostcmd", "Runner.ValidateStages"},
	{"internal/scan/output", "ParseOutput"},
	{"internal/session/guidancedelivery", "Service.Emit"},
	{"internal/session/guidancedelivery", "Service.EmitEager"},
}

var renderIOImports = []string{"net", "net/http", "database/sql", "os/exec", "syscall"}

var renderImports = []invariants.Invariant{{
	Name:    "render-path-files-avoid-io-imports",
	Anchors: renderAnchors(),
	Check: func(r *invariants.Run) {
		packages := map[string]map[string][]renderFunction{}
		for _, entry := range renderEntries {
			if packages[entry.dir] == nil {
				packages[entry.dir] = renderFunctions(r, entry.dir)
			}
			functions := packages[entry.dir]
			r.Expect(len(functions[entry.function]), entry.dir+"."+entry.function)
			checkRenderPath(r, entry.function, functions)
		}
	},
}}

func renderAnchors() []invariants.Anchor {
	anchors := make([]invariants.Anchor, 0, len(renderEntries))
	for _, entry := range renderEntries {
		anchors = append(anchors, invariants.Func(entry.dir, entry.function))
	}
	return anchors
}

type renderFunction struct {
	file *invariants.GoFile
	fn   *ast.FuncDecl
}

func renderFunctions(r *invariants.Run, dir string) map[string][]renderFunction {
	functions := map[string][]renderFunction{}
	for _, file := range r.GoFiles(func(f *invariants.GoFile) bool {
		return !f.IsTest && f.Dir == dir
	}, dir) {
		file.Funcs(func(fn *ast.FuncDecl) {
			name := invariants.FuncName(fn)
			functions[name] = append(functions[name], renderFunction{file, fn})
		})
	}
	return functions
}

// Report I/O imports in the files reached by direct calls in the package.
// This deliberately checks whole files and does not claim runtime purity.
func checkRenderPath(r *invariants.Run, entry string, functions map[string][]renderFunction) {
	visited := map[string]bool{}
	files := map[*invariants.GoFile]bool{}
	var walk func(string)
	walk = func(name string) {
		if visited[name] {
			return
		}
		visited[name] = true
		for _, item := range functions[name] {
			files[item.file] = true
			item.file.Calls(item.fn.Body, func(call *ast.CallExpr, _, _ string) {
				if callee := renderLocalCallee(item.fn, ast.Unparen(call.Fun)); callee != "" {
					walk(callee)
				}
			})
		}
	}
	walk(entry)
	for file := range files {
		for _, path := range renderIOImports {
			if file.Imports(path) {
				r.Report("%s: %s reaches file with I/O import %s", file.Rel, entry, path)
			}
		}
	}
}

func renderLocalCallee(fn *ast.FuncDecl, callee ast.Expr) string {
	switch callee := callee.(type) {
	case *ast.Ident:
		return callee.Name
	case *ast.SelectorExpr:
		if fn.Recv != nil {
			receiver := fn.Recv.List[0].Names
			if id, ok := ast.Unparen(callee.X).(*ast.Ident); ok && len(receiver) > 0 && id.Name == receiver[0].Name {
				return invariants.ReceiverType(fn) + "." + callee.Sel.Name
			}
		}
	case *ast.IndexExpr:
		return renderLocalCallee(fn, callee.X)
	case *ast.IndexListExpr:
		return renderLocalCallee(fn, callee.X)
	}
	return ""
}
