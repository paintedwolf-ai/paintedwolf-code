package forbiddencalls

import (
	"go/ast"
	"go/token"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

// gateProseScope decides gates and guidance; it branches on machine state.
var gateProseScope = []string{
	"internal/conditions", "internal/confine", "internal/gate", "internal/guidance",
	"internal/oar", "internal/secretmint", "internal/settings", "internal/tools",
}

var matcherPackages = map[string]string{"strings": "strings", "bytes": "bytes", "regexp": "regexp"}

var sessions = []invariants.Invariant{{
	// Compaction views are published by the history service's three
	// publication paths only.
	Name:    "compaction-has-one-writer",
	Anchors: []invariants.Anchor{invariants.Func("internal/session/history", "Service.writeCompactionView")},
	Exempt: []invariants.Exemption{
		{Key: "internal/session/history.Service.runBackgroundCompactionPage", Reason: "background compaction publishes its page"},
		{Key: "internal/session/history.Service.forceCompact", Reason: "forced compaction publishes its view"},
		{Key: "internal/session/history.Service.publishReductionSynthesis", Reason: "reduction synthesis publishes its view"},
	},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Production, "internal", "cmd", "pkg", "config", "test") {
			for _, decl := range f.AST.Decls {
				site := f.Dir + ".<package>"
				if fn, ok := decl.(*ast.FuncDecl); ok {
					site = f.Dir + "." + invariants.FuncName(fn)
				}
				f.Calls(decl, func(call *ast.CallExpr, _, _ string) {
					if invariants.CalleeName(call) == "writeCompactionView" && !r.Exempt(site) {
						r.ReportAt(call.Pos(), "%s writes a compaction view outside the history service's publication paths", site)
					}
				})
			}
		}
	},
}, {
	// Gates branch on machine state, never on prose. Every matcher call is
	// classified, so a prose branch cannot get in through an unknown one.
	Name:    "gate-layer-branches-on-machine-state-not-prose",
	Anchors: []invariants.Anchor{invariants.Package("internal/gate")},
	Check: func(r *invariants.Run) {
		for name := range matcherNeedleArgs {
			if matcherNonNeedle[name] {
				r.Report("matcher %s is classified as both needle-taking and needle-free", name)
			}
		}
		for _, f := range r.GoFiles(invariants.Production, gateProseScope...) {
			for _, finding := range scanProseNeedles(f) {
				r.ReportAt(finding.pos, "%s", finding.what)
			}
		}
	},
}, {
	// Loop wake keys on typed facts such as HumanApprovalAwaiting, never a
	// phase name.
	Name:    "loop-wake-does-not-key-on-phase-names",
	Anchors: []invariants.Anchor{invariants.Package("internal/coordinator/loopwake")},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Production, "internal/coordinator/loopwake") {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				bin, ok := n.(*ast.BinaryExpr)
				if !ok || bin.Op != token.EQL && bin.Op != token.NEQ {
					return true
				}
				_, xLit := invariants.StringLit(bin.X)
				_, yLit := invariants.StringLit(bin.Y)
				if (isCurrentPhase(bin.X) && yLit) || (isCurrentPhase(bin.Y) && xLit) {
					r.ReportAt(bin.Pos(), "compares CurrentPhase with a phase name")
				}
				return true
			})
		}
	},
}, {
	// Visual promotion, dedup and report sections decide on machine state:
	// evidence handles, ids and data presence.
	Name:    "visual-durable-path-has-no-heuristics",
	Anchors: []invariants.Anchor{invariants.Package("internal/visual"), invariants.Package("internal/report")},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Production, "internal/visual", "internal/report", "internal/guidance", "internal/api") {
			base := path.Base(f.Rel)
			gated := invariants.Under(f, "internal/visual") || contains([]string{"sections.go", "report.go", "input.go", "closeout_present.go", "workflow_report_handler.go"}, base)
			if !gated || strings.Contains(base, "fixture") {
				continue
			}
			f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
				if pkg != "strings" || !contains([]string{"Contains", "ContainsAny", "ContainsRune", "HasPrefix", "HasSuffix", "Index", "IndexAny"}, name) {
					return
				}
				for _, arg := range call.Args {
					if needle, ok := invariants.StringLit(arg); ok && looksLikeProseNeedle(needle) {
						r.ReportAt(arg.Pos(), "prose matcher strings.%s(%q); branch on machine state", name, needle)
					}
				}
			})
			if base == "sections.go" || base == "report.go" || base == "closeout_present.go" {
				for _, banned := range []string{"WorkflowID ==", "workflowID ==", "workflow_id =="} {
					if strings.Contains(string(f.Src), banned) {
						r.Report("%s branches report sections on a workflow id (%q)", f.Rel, banned)
					}
				}
			}
		}
	},
}}

func isCurrentPhase(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "CurrentPhase"
}

type proseFinding struct {
	pos  token.Pos
	what string
}

func scanProseNeedles(f *invariants.GoFile) []proseFinding {
	var out []proseFinding
	ast.Inspect(f.AST, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			pkg, name, ok := f.Qualified(node.Fun)
			if !ok || matcherPackages[pkg] == "" {
				return true
			}
			positions, classified := matcherNeedleArgs[name]
			if !classified {
				if !matcherNonNeedle[name] {
					out = append(out, proseFinding{node.Pos(), "unclassified matcher " + pkg + "." + name +
						": add it to matcherNeedleArgs with the positions it compares, or to matcherNonNeedle"})
				}
				return true
			}
			isProse := looksLikeProseNeedle
			if pkg == "regexp" {
				isProse = looksLikeProseRegex
			}
			for _, arg := range needleArgs(node, positions) {
				if needle, ok := invariants.StringLit(arg); ok && isProse(needle) {
					out = append(out, proseFinding{arg.Pos(), "prose needle " + pkg + "." + name + "(" + strconv.Quote(needle) +
						"); branch on machine state or hoist a host-written marker to a named constant"})
				}
			}
		case *ast.RangeStmt:
			// Range variables hide literals from the call-argument scan.
			if values := proseSliceLiteral(node.X); len(values) >= 2 {
				out = append(out, proseFinding{node.X.Pos(), "ranges over the prose list (" + strings.Join(values, ", ") +
					"); a compared vocabulary belongs in catalog YAML"})
			}
		}
		return true
	})
	return out
}

func proseSliceLiteral(expr ast.Expr) []string {
	cl, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	arr, ok := cl.Type.(*ast.ArrayType)
	if !ok || arr.Len != nil {
		return nil
	}
	if elem, ok := arr.Elt.(*ast.Ident); !ok || elem.Name != "string" {
		return nil
	}
	var prose []string
	for _, e := range cl.Elts {
		value, ok := invariants.StringLit(e)
		if !ok {
			return nil
		}
		if looksLikeProseNeedle(value) {
			prose = append(prose, strconv.Quote(value))
		}
	}
	return prose
}

// needleAllArgs includes every argument of a variadic matcher.
const needleAllArgs = -1

// matcherNeedleArgs identifies argument positions inspected for prose literals.
var matcherNeedleArgs = map[string][]int{
	"Contains":     {1},
	"ContainsAny":  {1},
	"ContainsRune": {1},
	"Count":        {1},
	"Cut":          {1},
	"CutPrefix":    {1},
	"CutSuffix":    {1},
	"EqualFold":    {0, 1}, // symmetric: either side may be the literal
	"HasPrefix":    {1},
	"HasSuffix":    {1},
	"Index":        {1},
	"IndexAny":     {1},
	"LastIndex":    {1},
	"LastIndexAny": {1},
	"Split":        {1},
	"SplitAfter":   {1},
	"SplitN":       {1},
	"Trim":         {1},
	"TrimLeft":     {1},
	"TrimPrefix":   {1},
	"TrimRight":    {1},
	"TrimSuffix":   {1},
	"NewReplacer":  {needleAllArgs}, // variadic old/new pairs
	"MustCompile":  {0},             // regexp: the pattern is the matcher
	"Compile":      {0},             // regexp: the pattern is the matcher
	"Replace":      {1},             // strings.Replace(s, old, new, n)
	"ReplaceAll":   {1},             // strings.ReplaceAll(s, old, new)
}

// matcherNonNeedle lists calls without string-pattern arguments.
var matcherNonNeedle = map[string]bool{
	"QuoteMeta":     true, // escapes literal regexp input; it performs no comparison
	"Clone":         true, // copies its input; it performs no comparison
	"Fields":        true,
	"FieldsFunc":    true, // splits on a rune predicate; there is no needle
	"FieldsSeq":     true, // splits on whitespace; there is no needle
	"IndexByte":     true, // byte argument, not a string needle
	"Join":          true,
	"NewReader":     true,
	"Repeat":        true,
	"ToLower":       true,
	"ToUpper":       true,
	"ToValidUTF8":   true, // second argument is the replacement, not a needle
	"TrimSpace":     true,
	"Equal":         true, // bytes.Equal compares byte slices
	"Compare":       true, // bytes.Compare compares byte slices
	"LastIndexByte": true, // byte argument, not a string needle
}

// Literal word spacing distinguishes prose from regex escapes and character classes.
var proseInsideRegex = regexp.MustCompile(`[A-Za-z]{2,} [A-Za-z]{2,}`)

func looksLikeProseRegex(pattern string) bool {
	return proseInsideRegex.MatchString(pattern)
}

func needleArgs(call *ast.CallExpr, positions []int) []ast.Expr {
	for _, p := range positions {
		if p == needleAllArgs {
			return call.Args
		}
	}
	var out []ast.Expr
	for _, p := range positions {
		if p >= 0 && p < len(call.Args) {
			out = append(out, call.Args[p])
		}
	}
	return out
}

// looksLikeProseNeedle flags English-ish needles used to infer intent from
// closeout/synthesis text. Technical tokens (extensions, mime, path fragments,
// HTML sniff) are machine-state checks and allowed.
func looksLikeProseNeedle(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	for _, ok := range []string{
		"image/", "application/", "text/", "http", "refs.json", "page#",
		"/.paintedwolf", "<!", "</", "bytes",
	} {
		if strings.Contains(lower, ok) {
			return false
		}
	}
	// File extensions / dotted tokens without spaces.
	if strings.HasPrefix(s, ".") && !strings.Contains(s, " ") {
		return false
	}
	// Structural markers carry no words: "## ", "- [ ] ", "```". Trimming a
	// markdown heading is reading document structure, not inferring intent, and
	// the space inside the marker must not make it read as English.
	if isPunctuationMarker(s) {
		return false
	}
	// Prose: whitespace (multi-word English) or sentence punctuation.
	if strings.Contains(s, " ") {
		return true
	}
	if strings.ContainsAny(s, ";!") && len(s) > 6 {
		return true
	}
	return false
}

// isPunctuationMarker reports whether s carries no letters or digits once
// whitespace is removed — a delimiter or markup token rather than words.
func isPunctuationMarker(s string) bool {
	seen := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
		seen = true
	}
	return seen
}
