package forbiddencalls

import (
	"go/ast"

	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/test/contract/laws"
)

var debugCapture = []laws.Rule{{
	// Capture filenames and their redirect variables are spelled once, in
	// internal/debugpaths.
	Name:    "debug-capture-names-live-in-debugpaths",
	Anchors: []laws.Anchor{laws.Func("internal/debugpaths", "Files"), laws.Decl("internal/debugpaths", "FullDebugEnv")},
	Check: func(r *laws.Run) {
		names := map[string]bool{debugpaths.FullDebugEnv: true, debugpaths.SessionDirEnv: true}
		files := debugpaths.Files()
		r.Expect(len(files), "debug capture catalog")
		for _, file := range files {
			for _, name := range []string{file.Name, file.FileEnv, file.EnableEnv} {
				if name != "" {
					names[name] = true
				}
			}
		}
		for _, f := range r.GoFiles(laws.Production, "internal", "cmd", "pkg", "config", "test") {
			if laws.Under(f, "internal/debugpaths") {
				continue
			}
			laws.StringLits(f.AST, func(lit *ast.BasicLit, value string) {
				if names[value] {
					r.ReportAt(lit.Pos(), "spells %s; use internal/debugpaths", value)
				}
			})
		}
	},
}}
