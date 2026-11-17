package forbiddencalls

import (
	"go/ast"
	"go/constant"
	"strings"

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

var debugSwitch = []laws.Rule{{
	// Every *DebugEnabled gate delegates to debugpaths.Enabled, which honours
	// the main switch, so LYCAON_DEBUG_ALL reaches every capture.
	Name:    "debug-gates-delegate-to-the-chokepoint",
	Anchors: []laws.Anchor{laws.Func("internal/debugpaths", "Enabled"), laws.Func("internal/debugpaths", "FullDebugEnabled")},
	Check: func(r *laws.Run) {
		enabled := r.FuncDecl("internal/debugpaths", "Enabled")
		honours := false
		ast.Inspect(enabled.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && laws.CalleeName(call) == "FullDebugEnabled" {
				honours = true
			}
			return !honours
		})
		if !honours {
			r.Report("debugpaths.Enabled must call FullDebugEnabled")
		}
		gates := 0
		for _, f := range r.GoFiles(laws.Production, "internal/observability") {
			f.Funcs(func(fn *ast.FuncDecl) {
				if fn.Recv != nil || !strings.HasSuffix(fn.Name.Name, "DebugEnabled") {
					return
				}
				gates++
				delegates := false
				f.Calls(fn.Body, func(_ *ast.CallExpr, pkg, name string) {
					delegates = delegates || pkg == "github.com/lycaon/lycaon/internal/debugpaths" && name == "Enabled"
				})
				if !delegates {
					r.ReportAt(fn.Pos(), "%s must return debugpaths.Enabled(...)", fn.Name.Name)
				}
			})
		}
		r.Expect(gates, "*DebugEnabled gates in internal/observability")
	},
}, {
	// Debug toggles are read through debugpaths, never os.Getenv.
	Name:    "no-direct-debug-toggle-getenv",
	Anchors: []laws.Anchor{laws.Decl("internal/debugpaths", "FullDebugEnv")},
	Check: func(r *laws.Run) {
		for _, pkg := range r.TypedPackages("internal", "cmd", "pkg", "config") {
			for _, f := range pkg.Files {
				if laws.Under(f, "internal/debugpaths") {
					continue
				}
				f.Calls(f.AST, func(call *ast.CallExpr, p, name string) {
					if p != "os" || name != "Getenv" || len(call.Args) != 1 {
						return
					}
					tv := pkg.Info.Types[call.Args[0]]
					if tv.Value == nil || tv.Value.Kind() != constant.String {
						return
					}
					env := constant.StringVal(tv.Value)
					if env == debugpaths.FullDebugEnv || strings.HasPrefix(env, "LYCAON_") && strings.HasSuffix(env, "_DEBUG") {
						r.ReportAt(call.Pos(), "os.Getenv(%q); read it through debugpaths", env)
					}
				})
			}
		}
	},
}}
