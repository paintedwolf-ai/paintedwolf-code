package forbiddencalls

import (
	"go/ast"
	"go/token"
	"go/types"
	"regexp"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
	"gopkg.in/yaml.v3"
)

var guidanceEmission = []laws.Rule{{
	// Agent feedback is emitted through registered hint codes and templates,
	// never as inline prose.
	Name: "guidance-no-inline-emission",
	Anchors: []laws.Anchor{
		laws.Func("internal/coordinator/kick", "KickEngine.QueuePendingText"),
		laws.Func("internal/guidance", "FormatCoordinatorNudge"),
	},
	Check: func(r *laws.Run) {
		scan := r.GuidanceEmission()
		for _, site := range scan.QueuePendingTextSites {
			r.Report("%s: QueuePendingText; queue a registered hint through the coordinator nudge", site)
		}
		for _, site := range scan.InlineCodeNudgeLiterals {
			r.Report("%s: inline \"Code: …\" nudge literal; register a hint and template", site)
		}
	},
}, {
	// Tool envelope packages carry hints by registered code.
	Name:    "tool-envelope-no-inline-hint-consts",
	Anchors: []laws.Anchor{laws.Func("internal/guidance", "EnvelopeHintMessage")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal/scan", "internal/repomap") {
			for _, decl := range f.AST.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.CONST {
					continue
				}
				for _, spec := range gen.Specs {
					value := spec.(*ast.ValueSpec)
					for i, name := range value.Names {
						if !strings.HasSuffix(name.Name, "Hint") || i >= len(value.Values) {
							continue
						}
						if _, ok := laws.StringLit(value.Values[i]); ok {
							r.ReportAt(name.Pos(), "const %s holds hint prose; use the hint registry", name.Name)
						}
					}
				}
			}
		}
	},
}}

// catalogCopyWindow is long enough not to collide with common identifiers.
const catalogCopyWindow = 40

// copyPlaceholderRE matches format verbs and template tags that interrupt
// otherwise identical copy.
var copyPlaceholderRE = regexp.MustCompile(`%[+\-# 0-9.]*[a-zA-Z]|\{\{.*?\}\}|\{%.*?%\}`)

var catalogCopy = []laws.Rule{{
	// Production Go renders catalog copy instead of restating it: nothing
	// renders both, so two copies drift apart unnoticed.
	Name:    "production-go-holds-no-catalog-copy",
	Anchors: []laws.Anchor{laws.Dir("lycaon/config/packs")},
	Exempt: []laws.Exemption{
		{Key: "internal/guidance/host_markers.go", Reason: "the single spelling of markers the host writes and reads back; the catalog and the constant are one vocabulary"},
		{Key: "internal/datamark/datamark.go", Reason: "the untrusted-retrieval teaching is rendered into every framed body as well as the prompt partial"},
	},
	Check: func(r *laws.Run) {
		windows := catalogCopyWindows(r)
		for _, f := range r.GoFiles(laws.Production, "internal") {
			laws.StringLits(f.AST, func(lit *ast.BasicLit, value string) {
				if source, ok := matchCatalogCopy(value, windows); ok && !r.Exempt(f.Rel) {
					r.ReportAt(lit.Pos(), "restates catalog copy from %s; render the catalog body", source)
				}
			})
		}
	},
}}

// catalogCopyWindows indexes the first window of every prose line and policy
// copy field in the stock packs.
func catalogCopyWindows(r *laws.Run) map[string]string {
	out := map[string]string{}
	add := func(text, source string) {
		text = normalizeCopy(text)
		if len(text) < catalogCopyWindow {
			return
		}
		if _, seen := out[text[:catalogCopyWindow]]; !seen {
			out[text[:catalogCopyWindow]] = source
		}
	}
	for _, file := range r.Tree("lycaon/config/packs", ".md", ".yaml") {
		rel := "lycaon/config/packs/" + file.Rel
		switch {
		case strings.HasSuffix(rel, ".md"):
			fenced := false
			for _, line := range strings.Split(file.Text(), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "```") {
					fenced = !fenced
					continue
				}
				// Fenced examples and tables share format boilerplate with code.
				if !fenced && line != "" && !strings.HasPrefix(line, "|") {
					add(line, rel)
				}
			}
		case strings.Contains(rel, "/policy/"):
			var unit map[string]any
			r.Must(yaml.Unmarshal(file.Bytes(), &unit))
			for _, key := range []string{"what", "cause", "why", "fix", "instead", "message", "title"} {
				if value, ok := unit[key].(string); ok {
					add(value, rel)
				}
			}
		}
	}
	r.Expect(len(out), "catalog copy windows")
	return out
}

func matchCatalogCopy(text string, windows map[string]string) (string, bool) {
	normalized := normalizeCopy(text)
	for i := 0; i+catalogCopyWindow <= len(normalized); i++ {
		if source, ok := windows[normalized[i:i+catalogCopyWindow]]; ok {
			return source, true
		}
	}
	return "", false
}

func normalizeCopy(text string) string {
	return strings.Join(strings.Fields(copyPlaceholderRE.ReplaceAllString(text, " ")), " ")
}

// Host decisions read declared codes and typed fields, never prose.
var prose = []laws.Rule{{
	// The MCP CallTool error bridge reads the declared code field.
	Name:    "mcp-error-bridge-reads-codes",
	Anchors: []laws.Anchor{laws.GoPath("internal/mcp/call_envelope.go")},
	Check: func(r *laws.Run) {
		f := r.GoFile("internal/mcp/call_envelope.go")
		f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
			if pkg == "strings" && (name == "Contains" || name == "ContainsAny" || name == "EqualFold") {
				r.ReportAt(call.Pos(), "strings.%s classifies error prose", name)
			}
		})
	},
}}

// coordinatorScanRoots run turns; their decisions branch on typed state.
var coordinatorScanRoots = []string{"internal/session", "internal/coordinator", "internal/workflow"}

var userTextProbes = []string{"Contains", "HasPrefix", "HasSuffix", "EqualFold", "MatchString"}

var userTextParams = []string{"userPrompt", "userMessage", "message", "text"}

var userText = []laws.Rule{{
	// Errors are classified by sentinel and errors.Is, never by their text.
	Name:    "no-error-text-parsing-in-coordinator-packages",
	Anchors: []laws.Anchor{laws.Package("internal/session"), laws.Package("internal/coordinator/loopwake"), laws.Package("internal/workflow")},
	Check: func(r *laws.Run) {
		errType := types.Universe.Lookup("error").Type().Underlying().(*types.Interface)
		for _, pkg := range r.TypedPackages(coordinatorScanRoots...) {
			for _, f := range pkg.Files {
				f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
					name := laws.CalleeName(call)
					if !slices.Contains([]string{"Contains", "HasPrefix", "HasSuffix", "MatchString"}, name) {
						return
					}
					for _, arg := range call.Args {
						inner, ok := ast.Unparen(arg).(*ast.CallExpr)
						if !ok || laws.CalleeName(inner) != "Error" || len(inner.Args) != 0 {
							continue
						}
						sel, ok := inner.Fun.(*ast.SelectorExpr)
						if !ok {
							continue
						}
						if t := pkg.Info.TypeOf(sel.X); t != nil && types.Implements(t, errType) {
							r.ReportAt(call.Pos(), "%s over an error's text; use typed sentinels and errors.Is", name)
						}
					}
				})
			}
		}
	},
}, {
	// A string probe of the user's text never feeds a behavior sink.
	Name:    "no-user-text-probe-to-behavior-sink",
	Anchors: []laws.Anchor{laws.Package("internal/session"), laws.Package("internal/workflow")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, coordinatorScanRoots...) {
			f.Funcs(func(fn *ast.FuncDecl) {
				params := map[string]bool{}
				for _, field := range fn.Type.Params.List {
					for _, name := range field.Names {
						if slices.Contains(userTextParams, name.Name) {
							params[name.Name] = true
						}
					}
				}
				probes, sinks := false, false
				f.Calls(fn.Body, func(call *ast.CallExpr, _, _ string) {
					name := laws.CalleeName(call)
					probes = probes || slices.Contains(userTextProbes, name) && len(call.Args) > 0 &&
						isUserText(call.Args[0], params) && !probesNewline(name, call)
					sinks = sinks || name == "Emit"
				})
				if probes && sinks {
					r.ReportAt(fn.Pos(), "%s probes user text and reaches a behavior sink", fn.Name.Name)
				}
			})
		}
	},
}}

func probesNewline(name string, call *ast.CallExpr) bool {
	if name != "Contains" || len(call.Args) < 2 {
		return false
	}
	value, _ := laws.StringLit(call.Args[1])
	return value == "\n"
}

func isUserText(expr ast.Expr, params map[string]bool) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return params[e.Name] || slices.Contains(userTextParams, e.Name)
	case *ast.CallExpr:
		switch laws.CalleeName(e) {
		case "TrimSpace", "ToLower", "ToUpper":
			return len(e.Args) > 0 && isUserText(e.Args[0], params)
		}
	case *ast.SelectorExpr:
		if id, ok := e.X.(*ast.Ident); ok {
			return params[id.Name] || slices.Contains(userTextParams, id.Name)
		}
	}
	return false
}
