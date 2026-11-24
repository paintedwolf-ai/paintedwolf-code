package contract

// AST inventory of structured reject emission sites, so every production reject
// code is registry-backed and renders as a structured Rejected:/Code: block
// rather than a bare string in a completed outcome.

import (
	"go/ast"
	"go/token"
	"go/types"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/pkg/testcorpus"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/guidancescan"
)

// toolRejectSite is one production reject construction: a reject carrier
// literal, safecmd.Reject, or a call to a function forwarding its code.
type toolRejectSite struct {
	Code string
	Rel  string
	Line int
}

// unresolvedRejectSite is an emission whose code is neither a constant, a
// forwarded parameter, nor a local variable assigned only constants.
type unresolvedRejectSite struct {
	Rel  string
	Line int
	Func string
	Expr string
}

type toolRejectInventory struct {
	Sites      []toolRejectSite
	Unresolved []unresolvedRejectSite
}

type toolRejectScanCacheEntry struct {
	once sync.Once
	inv  toolRejectInventory
	err  error
}

var toolRejectScanCache sync.Map // abs lycaon root → *toolRejectScanCacheEntry

// scanToolRejectEmission inventories reject emission sites under lycaon/internal.
func scanToolRejectEmission(lycaonRoot string) (toolRejectInventory, error) {
	abs, err := filepath.Abs(lycaonRoot)
	if err != nil {
		return toolRejectInventory{}, err
	}
	raw, _ := toolRejectScanCache.LoadOrStore(abs, &toolRejectScanCacheEntry{})
	entry := raw.(*toolRejectScanCacheEntry)
	entry.once.Do(func() {
		entry.inv, entry.err = buildToolRejectInventory(abs)
	})
	return entry.inv, entry.err
}

func buildToolRejectInventory(lycaonRoot string) (toolRejectInventory, error) {
	consts, err := scanHintShapedStringConsts(lycaonRoot)
	if err != nil {
		return toolRejectInventory{}, err
	}
	corp, err := contractcheck.LoadGoASTCorpus(lycaonRoot)
	if err != nil {
		return toolRejectInventory{}, err
	}
	var production []testcorpus.GoFile
	for _, gf := range corp.Files() {
		if !gf.IsTest && strings.HasPrefix(gf.Rel, "internal/") {
			production = append(production, gf)
		}
	}
	carriers := rejectCarrierTypes(production)
	forwarders := rejectForwarders(production, carriers)
	var inv toolRejectInventory
	for _, gf := range production {
		dir := path.Dir(gf.Rel)
		for _, decl := range gf.AST.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			_, isForwarder := forwarders[forwarderKey{dir: dir, name: fn.Name.Name}]
			params := paramIndexes(fn.Type)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				var codeExpr ast.Expr
				switch node := n.(type) {
				case *ast.CompositeLit:
					if !carriers[contractcheck.CompositeLitTypeName(node.Type)] {
						return true
					}
					codeExpr = literalCodeValue(node)
				case *ast.CallExpr:
					arg, ok := rejectCodeArg(node, gf, forwarders)
					if !ok {
						return true
					}
					codeExpr = arg
				default:
					return true
				}
				if codeExpr == nil {
					return true
				}
				line := corp.Fset.Position(n.Pos()).Line
				codes, resolved := resolveRejectCodes(codeExpr, fn.Body, consts)
				if !resolved {
					if id, ok := unwrapConversion(codeExpr).(*ast.Ident); ok && isForwarder {
						if _, isParam := params[id.Name]; isParam {
							return true
						}
					}
					inv.Unresolved = append(inv.Unresolved, unresolvedRejectSite{Rel: gf.Rel, Line: line, Func: fn.Name.Name, Expr: types.ExprString(codeExpr)})
					return true
				}
				for _, code := range codes {
					if guidancescan.HintCodeShape.MatchString(code) {
						inv.Sites = append(inv.Sites, toolRejectSite{Code: code, Rel: gf.Rel, Line: line})
					}
				}
				return true
			})
		}
	}
	return inv, nil
}

// resolveRejectCodes resolves a constant code, or a local variable whose every
// assignment in body is a constant code.
func resolveRejectCodes(expr ast.Expr, body *ast.BlockStmt, consts map[string]string) ([]string, bool) {
	if code := constantRejectCode(expr, consts); code != "" {
		return []string{code}, true
	}
	id, ok := unwrapConversion(expr).(*ast.Ident)
	if !ok {
		return nil, false
	}
	var codes []string
	resolved := true
	assigned := false
	note := func(rhs ast.Expr) {
		assigned = true
		if code := constantRejectCode(rhs, consts); code != "" {
			codes = append(codes, code)
		} else {
			resolved = false
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch st := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range st.Lhs {
				if l, ok := lhs.(*ast.Ident); ok && l.Name == id.Name {
					if len(st.Rhs) != len(st.Lhs) {
						resolved = false
						continue
					}
					note(st.Rhs[i])
				}
			}
		case *ast.ValueSpec:
			for i, name := range st.Names {
				if name.Name == id.Name && i < len(st.Values) {
					note(st.Values[i])
				}
			}
		case *ast.RangeStmt:
			if l, ok := st.Value.(*ast.Ident); ok && l.Name == id.Name {
				resolved = false
			}
		}
		return true
	})
	return codes, resolved && assigned
}

// isSafecmdRejectCall reports safecmd.Reject(...) or Reject(...) inside package safecmd.
func isSafecmdRejectCall(fun ast.Expr, filePackage string) bool {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name == "Reject" && filePackage == "safecmd"
	case *ast.SelectorExpr:
		if f.Sel == nil || f.Sel.Name != "Reject" {
			return false
		}
		pkg, ok := f.X.(*ast.Ident)
		return ok && pkg.Name == "safecmd"
	default:
		return false
	}
}

// constantRejectCode resolves a code written as a literal, a SCREAMING_SNAKE
// constant, or a conversion of either.
func constantRejectCode(expr ast.Expr, consts map[string]string) string {
	if expr == nil {
		return ""
	}
	expr = unwrapConversion(expr)
	if s := contractcheck.AstStringLit(expr); s != "" {
		return s
	}
	switch v := expr.(type) {
	case *ast.Ident:
		return consts[v.Name]
	case *ast.SelectorExpr:
		return consts[v.Sel.Name]
	}
	return ""
}

type hintConstCacheEntry struct {
	once   sync.Once
	consts map[string]string
	err    error
}

var hintConstCache sync.Map // abs lycaon root → *hintConstCacheEntry

// scanHintShapedStringConsts maps const identifier → value for SCREAMING_SNAKE
// string constants under internal/ (used to resolve Code: FooCode fields).
func scanHintShapedStringConsts(lycaonRoot string) (map[string]string, error) {
	abs, err := filepath.Abs(lycaonRoot)
	if err != nil {
		return nil, err
	}
	raw, _ := hintConstCache.LoadOrStore(abs, &hintConstCacheEntry{})
	entry := raw.(*hintConstCacheEntry)
	entry.once.Do(func() {
		entry.consts, entry.err = buildHintShapedStringConsts(abs)
	})
	return entry.consts, entry.err
}

func buildHintShapedStringConsts(lycaonRoot string) (map[string]string, error) {
	corp, err := contractcheck.LoadGoASTCorpus(lycaonRoot)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, gf := range corp.Files() {
		if gf.IsTest || !strings.HasPrefix(gf.Rel, "internal/") {
			continue
		}
		for _, decl := range gf.AST.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if name == nil || i >= len(vs.Values) {
						continue
					}
					val := contractcheck.AstStringLit(vs.Values[i])
					if !guidancescan.HintCodeShape.MatchString(val) {
						continue
					}
					out[name.Name] = val
				}
			}
		}
	}
	return out, nil
}
