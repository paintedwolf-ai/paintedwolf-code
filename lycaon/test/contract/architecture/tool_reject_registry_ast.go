package contract

// AST inventory of structured reject emission sites, so every production reject
// code is registry-backed and renders as a structured Rejected:/Code: block
// rather than a bare string in a completed outcome.

import (
	"go/ast"
	"go/token"
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

type toolRejectScanCacheEntry struct {
	once  sync.Once
	sites []toolRejectSite
	err   error
}

var toolRejectScanCache sync.Map // abs lycaon root → *toolRejectScanCacheEntry

// scanToolRejectCodes finds resolved Code values at reject emission sites under lycaon/internal.
func scanToolRejectCodes(lycaonRoot string) ([]toolRejectSite, error) {
	abs, err := filepath.Abs(lycaonRoot)
	if err != nil {
		return nil, err
	}
	raw, _ := toolRejectScanCache.LoadOrStore(abs, &toolRejectScanCacheEntry{})
	entry := raw.(*toolRejectScanCacheEntry)
	entry.once.Do(func() {
		entry.sites, entry.err = buildToolRejectCodes(abs)
	})
	return entry.sites, entry.err
}

func buildToolRejectCodes(lycaonRoot string) ([]toolRejectSite, error) {
	consts, err := scanHintShapedStringConsts(lycaonRoot)
	if err != nil {
		return nil, err
	}
	corp, err := contractcheck.LoadGoASTCorpus(lycaonRoot)
	if err != nil {
		return nil, err
	}
	var production []testcorpus.GoFile
	for _, gf := range corp.Files() {
		if !gf.IsTest && strings.HasPrefix(gf.Rel, "internal/") {
			production = append(production, gf)
		}
	}
	carriers := rejectCarrierTypes(production)
	forwarders := rejectForwarders(production, carriers)
	var sites []toolRejectSite
	for _, gf := range production {
		ast.Inspect(gf.AST, func(n ast.Node) bool {
			var code string
			switch node := n.(type) {
			case *ast.CompositeLit:
				if !carriers[contractcheck.CompositeLitTypeName(node.Type)] {
					return true
				}
				code = constantRejectCode(literalCodeValue(node), consts)
			case *ast.CallExpr:
				arg, ok := rejectCodeArg(node, gf, forwarders)
				if !ok {
					return true
				}
				code = constantRejectCode(arg, consts)
			default:
				return true
			}
			if code == "" || !guidancescan.HintCodeShape.MatchString(code) {
				return true
			}
			pos := corp.Fset.Position(n.Pos())
			sites = append(sites, toolRejectSite{Code: code, Rel: gf.Rel, Line: pos.Line})
			return true
		})
	}
	return sites, nil
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
