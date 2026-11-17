package forbiddencalls

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
	"unicode"

	"github.com/lycaon/lycaon/test/contract/laws"
)

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
