package forbiddencalls

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
	"unicode"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// modelPolicyWrites change device configuration.
var modelPolicyWrites = []string{"PutModelPolicy", "ApplyModelPolicy", "ApplyGlobal", "ApplyProject"}

var hostDoors = []laws.Rule{{
	// A commit's paths come from what this session wrote: a shared working
	// tree holds other writers' work, so whole-tree dirtiness is read only
	// where it is narrowed to session authorship.
	Name:    "dirty-tree-reaches-commits-only-through-authorship",
	Anchors: []laws.Anchor{laws.GoPath("internal/tools/native/git_commit_staging.go")},
	Exempt:  []laws.Exemption{{Key: "internal/tools/native/git_commit_staging.go", Reason: "narrows whole-tree dirtiness to session authorship"}},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(func(f *laws.GoFile) bool { return !f.IsTest && f.Dir == "internal/tools/native" }, "internal/tools/native") {
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				if laws.CalleeName(call) == "ChangedPaths" && !r.Exempt(f.Rel) {
					r.ReportAt(call.Pos(), "reads ChangedPaths; narrow through session authorship in git_commit_staging.go")
				}
			})
		}
	},
}, {
	// Boot, wipe and recovery never write model policy: device configuration
	// survives them.
	Name:    "device-config-write-door",
	Anchors: []laws.Anchor{laws.Package("internal/app"), laws.Package("internal/bootrecovery"), laws.Package("internal/db")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal/app", "internal/bootrecovery", "internal/db") {
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return
				}
				if contains(modelPolicyWrites, sel.Sel.Name) || isPolicyStoreWrite(sel, sel.Sel.Name) {
					r.ReportAt(call.Pos(), "%s writes device configuration from boot, wipe or recovery", sel.Sel.Name)
				}
			})
		}
	},
}, {
	// Dotted-path reads of workflow variables go through conditions.DotPathGet.
	Name:    "dot-path-reads-have-one-authority",
	Anchors: []laws.Anchor{laws.Func("internal/conditions", "DotPathGet")},
	Exempt:  []laws.Exemption{{Key: "internal/detectionpack/action_semantics.go:lookupArgPath", Reason: "Sigma rule field lookup over model-authored tool arguments"}},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal") {
			if laws.Under(f, "internal/conditions") {
				continue
			}
			f.Funcs(func(fn *ast.FuncDecl) {
				if walksDotSeparatedPath(f, fn.Body) && !r.Exempt(f.Rel+":"+fn.Name.Name) {
					r.ReportAt(fn.Pos(), "%s walks a dotted path; call conditions.DotPathGet", fn.Name.Name)
				}
			})
		}
	},
}, {
	// HTTP clients carry deadlines: httpclient.Bounded, Streaming or Download.
	Name:    "no-unbounded-http-clients",
	Anchors: []laws.Anchor{laws.Package("internal/httpclient")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal") {
			if laws.Under(f, "internal/httpclient") {
				continue
			}
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				if pkg == "net/http" && contains([]string{"DefaultClient", "Get", "Post", "Head", "PostForm"}, name) {
					r.ReportAt(sel.Pos(), "http.%s has no deadline; use internal/httpclient", name)
				}
			})
			ast.Inspect(f.AST, func(n ast.Node) bool {
				if lit, ok := n.(*ast.CompositeLit); ok && len(lit.Elts) == 0 {
					if pkg, name, ok := f.Qualified(lit.Type); ok && pkg == "net/http" && name == "Client" {
						r.ReportAt(lit.Pos(), "http.Client{} has no deadline; use internal/httpclient")
					}
				}
				return true
			})
		}
	},
}}

// walksDotSeparatedPath detects a string-keyed dotted-path read: a split on
// "." and an assertion to map[string]any.
func walksDotSeparatedPath(f *laws.GoFile, body *ast.BlockStmt) bool {
	splitsOnDot, assertsStringMap := false, false
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			if pkg, name, ok := f.Qualified(node.Fun); ok && pkg == "strings" && name == "Split" && len(node.Args) == 2 {
				sep, _ := laws.StringLit(node.Args[1])
				splitsOnDot = splitsOnDot || sep == "."
			}
		case *ast.TypeAssertExpr:
			if mp, ok := node.Type.(*ast.MapType); ok {
				if key, ok := mp.Key.(*ast.Ident); ok && key.Name == "string" && isEmptyInterface(mp.Value) {
					assertsStringMap = true
				}
			}
		}
		return true
	})
	return splitsOnDot && assertsStringMap
}

// isEmptyInterface recognizes interface{} and any.
func isEmptyInterface(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.InterfaceType:
		return t.Methods == nil || len(t.Methods.List) == 0
	case *ast.Ident:
		return t.Name == "any"
	}
	return false
}

func isPolicyStoreWrite(sel *ast.SelectorExpr, name string) bool {
	if name != "PutGlobal" && name != "PutProject" {
		return false
	}
	switch recv := sel.X.(type) {
	case *ast.Ident:
		return recv.Name == "Policy" || recv.Name == "policy"
	case *ast.SelectorExpr:
		return recv.Sel.Name == "Policy"
	default:
		return false
	}
}

// providerEgressChain is each web research step and the guarded call it makes.
var providerEgressChain = [][2]string{
	{"doProviderHTTP", "providerEgressClient"},
	{"providerEgressClient", "resolveProviderEgressIPs"},
	{"validateProviderEndpoint", "ipProviderEgress"},
	{"ValidateProviderConfigEndpoint", "resolveProviderEgressIPs"},
	{"providerEgressClient", "PinnedTransport"},
}

var egressSeams = []laws.Rule{{
	// Web research reaches providers through the guarded transport, which
	// resolves, admits and pins every address.
	Name:    "provider-http-uses-the-egress-guard",
	Anchors: []laws.Anchor{laws.Func("internal/egress", "PinnedTransport"), laws.Package("internal/webresearch")},
	Check: func(r *laws.Run) {
		for _, step := range providerEgressChain {
			f, fn := r.FuncNamed("internal/webresearch", step[0])
			calls := false
			f.Calls(fn.Body, func(call *ast.CallExpr, _, _ string) { calls = calls || laws.CalleeName(call) == step[1] })
			if !calls {
				r.ReportAt(fn.Pos(), "%s must call %s", step[0], step[1])
			}
		}
		for _, f := range r.GoFiles(laws.Production, "internal/webresearch") {
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				if pkg == "net/http" && (name == "DefaultClient" || name == "ProxyFromEnvironment") {
					r.ReportAt(sel.Pos(), "http.%s bypasses the guarded transport", name)
				}
			})
		}
	},
}, {
	// The argv tokenizer and renderer share one whitespace vocabulary,
	// defined in internal/argv/whitespace.go.
	Name:    "argv-has-one-whitespace-vocabulary",
	Anchors: []laws.Anchor{laws.GoPath("internal/argv/whitespace.go")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal/argv") {
			if f.Rel == "internal/argv/whitespace.go" {
				continue
			}
			ast.Inspect(f.AST, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.CallExpr:
					if name := laws.CalleeName(v); contains([]string{"TrimSpace", "TrimLeft", "TrimRight", "IsSpace", "Fields"}, name) {
						r.ReportAt(v.Pos(), "calls %s; ask trimCommandLine, trimmedEdge or argSeparator", name)
					}
				case *ast.BasicLit:
					if v.Kind == token.CHAR && isWhitespaceRune(v.Value) {
						r.ReportAt(v.Pos(), "names the whitespace literal %s; ask argSeparator or trimmedEdge", v.Value)
					}
				}
				return true
			})
		}
	},
}, {
	// A suite that qualifies installed bytes is not an HTTP journey, so a
	// security digest never selects or skips around it.
	Name:    "installed-helper-suite-is-not-a-security-journey",
	Anchors: []laws.Anchor{laws.Dir("lycaon/test/security")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Tests, "test/security") {
			if strings.Contains(string(f.Src), "installedhelper") {
				r.Report("%s qualifies the installed helper inside the HTTP journey layer", f.Rel)
			}
		}
	},
}}

// isWhitespaceRune recognizes a rune literal for a space character.
func isWhitespaceRune(lit string) bool {
	unquoted, err := strconv.Unquote(lit)
	runes := []rune(unquoted)
	return err == nil && len(runes) == 1 && unicode.IsSpace(runes[0])
}
