package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// The scan checks inline literals and requires every matcher call to be classified.
func TestGateLayerBranchesOnMachineStateNotProse(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, rel := range gateProseScopeDirs {
		dir := filepath.Join(root, filepath.FromSlash(rel))
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return walkErr
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			display, _ := filepath.Rel(root, path)
			for _, finding := range scanProseNeedles(fset, f, display) {
				t.Error(finding)
			}
			return nil
		})
		testutil.FailErr(t, "walk "+rel, err)
	}
}

// Scan scope is explicit and checked for missing directories.
var gateProseScopeDirs = []string{
	"lycaon/internal/conditions",
	"lycaon/internal/confine",
	"lycaon/internal/gate",
	"lycaon/internal/guidance",
	"lycaon/internal/oar",
	"lycaon/internal/secretmint",
	"lycaon/internal/settings",
	"lycaon/internal/tools",
}

func TestGateProseScopeDirsExist(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var missing []string
	for _, rel := range gateProseScopeDirs {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || !info.IsDir() {
			missing = append(missing, rel)
		}
	}
	contractcheck.FailViolations(t, "gateProseScopeDirs names directories that no longer exist — the prose "+
		"scan walks nothing there and passes vacuously", missing)
}

var matcherPackages = map[string]bool{"strings": true, "bytes": true, "regexp": true}

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
	"EqualFold":    {0, 1}, // symmetric: either side may be the literal
	"HasPrefix":    {1},
	"HasSuffix":    {1},
	"Index":        {1},
	"IndexAny":     {1},
	"LastIndex":    {1},
	"Split":        {1},
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
	"Fields":        true,
	"FieldsFunc":    true, // splits on a rune predicate; there is no needle
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

func scanProseNeedles(fset *token.FileSet, f *ast.File, rel string) []string {
	var out []string
	at := func(pos token.Pos) string {
		return rel + ":" + strconv.Itoa(fset.Position(pos).Line)
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			pkg, name, ok := matcherCallName(node)
			if !ok {
				return true
			}
			positions, classified := matcherNeedleArgs[name]
			if !classified {
				if matcherNonNeedle[name] {
					return true
				}
				out = append(out, at(node.Pos())+": unclassified matcher "+pkg+"."+name+
					" — add it to matcherNeedleArgs with the argument positions it compares "+
					"against, or to matcherNonNeedle when it compares against nothing. An "+
					"unclassified matcher is how a prose branch gets in unseen")
				return true
			}
			isProse := looksLikeProseNeedle
			if pkg == "regexp" {
				isProse = looksLikeProseRegex
			}
			for _, arg := range needleArgs(node, positions) {
				needle, ok := stringLiteral(arg)
				if !ok || !isProse(needle) {
					continue
				}
				out = append(out, at(arg.Pos())+
					": prose needle "+pkg+"."+name+"("+strconv.Quote(needle)+
					") — branch on machine state, or hoist a host-written marker to a named constant")
			}
		case *ast.RangeStmt:
			// Range variables hide literals from the call-argument scan.
			values := proseSliceLiteral(node.X)
			if len(values) < 2 {
				return true
			}
			out = append(out, at(node.X.Pos())+
				": prose needle list ranged over ("+strings.Join(values, ", ")+
				") — a vocabulary the gate compares against belongs in catalogue YAML")
		}
		return true
	})
	return out
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

func matcherCallName(call *ast.CallExpr) (pkg, name string, ok bool) {
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel {
		return "", "", false
	}
	ident, isIdent := sel.X.(*ast.Ident)
	if !isIdent || !matcherPackages[ident.Name] {
		return "", "", false
	}
	return ident.Name, sel.Sel.Name, true
}

func TestMatcherClassificationIsUnambiguous(t *testing.T) {
	t.Parallel()
	var both []string
	for name := range matcherNeedleArgs {
		if matcherNonNeedle[name] {
			both = append(both, name)
		}
	}
	sort.Strings(both)
	contractcheck.FailViolations(t, "matcher classified as both needle-taking and needle-free", both)
}

func stringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
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
	elem, ok := arr.Elt.(*ast.Ident)
	if !ok || elem.Name != "string" {
		return nil
	}
	var prose []string
	for _, e := range cl.Elts {
		value, ok := stringLiteral(e)
		if !ok {
			return nil
		}
		if looksLikeProseNeedle(value) {
			prose = append(prose, strconv.Quote(value))
		}
	}
	return prose
}
