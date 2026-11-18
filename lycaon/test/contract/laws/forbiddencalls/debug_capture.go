package forbiddencalls

import (
	"go/ast"
	"go/constant"
	"go/token"
	"strings"

	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
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

var overlayDir = []laws.Rule{{
	// Protected settings paths share one overlay directory definition.
	Name:    "overlay-dir-name-has-one-definition",
	Anchors: []laws.Anchor{laws.Func("internal/settingsoverlay", "DirName"), laws.GoPath("internal/settingsoverlay/basenames.go")},
	Check: func(r *laws.Run) {
		name := settingsoverlay.DirName()
		for _, f := range r.GoFiles(laws.Production, "internal", "cmd", "pkg", "config", "test") {
			if f.Rel == "internal/settingsoverlay/basenames.go" {
				continue
			}
			laws.StringLits(f.AST, func(lit *ast.BasicLit, value string) {
				if value == name {
					r.ReportAt(lit.Pos(), "spells the overlay directory %q; use settingsoverlay.DirName()", name)
				}
			})
		}
	},
}}

// guardedMarkers are the host-written literals distinctive enough to scan
// for; the shorter ones reach Den through the same generated module.
var guardedMarkers = []string{hostmarker.VerbatimHeadTail, hostmarker.CompactionBannerOpen, hostmarker.Rejected}

// markerBoundaries precede a marker spelled as text: a quote or a regex
// anchor. An identifier character, space or brace makes it a field name.
const markerBoundaries = "\"`'^"

var frontendText = []laws.Rule{{
	Name:    "host-markers-have-one-spelling",
	Anchors: []laws.Anchor{laws.Package("internal/hostmarker")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal") {
			if laws.Under(f, "internal/hostmarker") {
				continue
			}
			for _, marker := range guardedMarkers {
				if spellsMarker(string(f.Src), marker) {
					r.Report("%s spells the host marker %q", f.Rel, marker)
				}
			}
		}
	},
}, {
	// Display text is cut by internal/runeclamp, on rune boundaries; a slice
	// with a marker appended can emit invalid UTF-8.
	Name:    "elision-markers-come-from-runeclamp",
	Anchors: []laws.Anchor{laws.Func("internal/runeclamp", "Clamp")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, laws.Source...) {
			if laws.Under(f, "internal/runeclamp") {
				continue
			}
			ast.Inspect(f.AST, func(n ast.Node) bool {
				bin, ok := n.(*ast.BinaryExpr)
				if ok && bin.Op == token.ADD && (isElisionMarker(bin.X) || isElisionMarker(bin.Y)) && (slicesString(bin.X) || slicesString(bin.Y)) {
					r.ReportAt(bin.Pos(), "cuts text by offset and appends an elision marker; use internal/runeclamp")
				}
				return true
			})
		}
	},
}, {
	// The visual store writes under the injected artifacts directory only.
	Name:    "visual-store-has-no-home-paths",
	Anchors: []laws.Anchor{laws.Package("internal/visual")},
	Check: func(r *laws.Run) {
		needles := []string{
			"filepath.Join(home", "filepath.Join(homedir",
			`"/.paintedwolf"`, `'.paintedwolf'`, "~/.paintedwolf",
			`"/.paintedwolf-dev"`, `'.paintedwolf-dev'`, "~/.paintedwolf-dev",
			`"/.lycaon"`, `'.lycaon'`, "~/.lycaon",
			`"/.paintedwolfcode"`, `'.paintedwolfcode'`, "~/.paintedwolfcode",
		}
		for _, f := range r.GoFiles(laws.Production, "internal/visual") {
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				if pkg == "os" && name == "UserHomeDir" {
					r.ReportAt(sel.Pos(), "os.UserHomeDir; inject the artifacts directory")
				}
			})
			for _, needle := range needles {
				if strings.Contains(string(f.Src), needle) {
					r.Report("%s hardcodes the home path %q; inject the artifacts directory", f.Rel, needle)
				}
			}
		}
	},
}}

func isElisionMarker(expr ast.Expr) bool {
	value, ok := laws.StringLit(expr)
	return ok && (value == "…" || value == "...")
}

func slicesString(expr ast.Expr) bool {
	slice, ok := expr.(*ast.SliceExpr)
	return ok && slice.High != nil
}

func spellsMarker(body, marker string) bool {
	for offset := 0; ; {
		idx := strings.Index(body[offset:], marker)
		if idx < 0 {
			return false
		}
		at := offset + idx
		offset = at + 1
		if at == 0 {
			return true
		}
		if strings.ContainsRune(markerBoundaries, rune(body[at-1])) {
			return true
		}
		// A `\n` escape immediately before the marker, inside one literal.
		if at >= 2 && body[at-1] == 'n' && body[at-2] == '\\' {
			return true
		}
	}
}
