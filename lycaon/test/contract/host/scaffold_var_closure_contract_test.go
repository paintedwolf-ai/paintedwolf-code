package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// hostVarKey is a key a SetHostVar call writes: an exact path, a dotted prefix
// completed at run time, or a named constant.
type hostVarKey struct {
	value    string
	prefix   bool
	constant string
}

// Every host variable a workflow writes has a reader: a use of its key outside
// the SetHostVar key argument that produced it.
func TestHostVarProducerConsumerClosure(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal")
	corpus, err := contractcheck.LoadGoASTCorpusMode(dir, parser.SkipObjectResolution)
	contractcheck.FailErr(t, "load internal corpus", err)
	files := corpus.Production()

	constants := map[string]string{}
	constantDecls := map[ast.Node]bool{}
	for _, file := range files {
		for _, decl := range file.AST.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value := spec.(*ast.ValueSpec)
				for i, name := range value.Names {
					if i >= len(value.Values) {
						continue
					}
					if lit, ok := value.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						text, _ := strconv.Unquote(lit.Value)
						constants[name.Name] = text
						constantDecls[lit] = true
						constantDecls[name] = true
					}
				}
			}
		}
	}

	keys := map[string]hostVarKey{}
	keyNodes := map[ast.Node]bool{}
	for _, file := range files {
		ast.Inspect(file.AST, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || calleeName(call.Fun) != "SetHostVar" || len(call.Args) < 2 {
				return true
			}
			arg := call.Args[1]
			if key, ok := hostVarKeyOf(arg, constants); ok {
				keys[key.value+"|"+key.constant] = key
				ast.Inspect(arg, func(inner ast.Node) bool {
					if inner != nil {
						keyNodes[inner] = true
					}
					return true
				})
			}
			return true
		})
	}
	if len(keys) == 0 {
		t.Fatal("no SetHostVar key found in lycaon/internal")
	}

	literals := map[string]bool{}
	identifiers := map[string]bool{}
	for _, file := range files {
		ast.Inspect(file.AST, func(n ast.Node) bool {
			if n == nil || keyNodes[n] {
				return true
			}
			switch node := n.(type) {
			case *ast.BasicLit:
				if node.Kind == token.STRING && !constantDecls[node] {
					if text, err := strconv.Unquote(node.Value); err == nil {
						literals[text] = true
					}
				}
			case *ast.Ident:
				if !constantDecls[node] {
					identifiers[node.Name] = true
				}
			}
			return true
		})
	}

	var orphans []string
	for _, key := range keys {
		if !hostVarKeyRead(key, literals, identifiers) {
			label := key.value
			if key.constant != "" {
				label = key.constant + " (" + key.value + ")"
			}
			orphans = append(orphans, label)
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Errorf("host variables written with no reader: %v", orphans)
	}
}

func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

func hostVarKeyOf(arg ast.Expr, constants map[string]string) (hostVarKey, bool) {
	switch a := arg.(type) {
	case *ast.BasicLit:
		if text, err := strconv.Unquote(a.Value); err == nil && a.Kind == token.STRING {
			return hostVarKey{value: text}, true
		}
	case *ast.BinaryExpr:
		if a.Op != token.ADD {
			return hostVarKey{}, false
		}
		left, ok := hostVarKeyOf(a.X, constants)
		if !ok || left.constant != "" {
			return hostVarKey{}, false
		}
		return hostVarKey{value: left.value, prefix: true}, true
	case *ast.Ident:
		if value, ok := constants[a.Name]; ok {
			return hostVarKey{value: value, constant: a.Name}, true
		}
	case *ast.SelectorExpr:
		if value, ok := constants[a.Sel.Name]; ok {
			return hostVarKey{value: value, constant: a.Sel.Name}, true
		}
	}
	return hostVarKey{}, false
}

func hostVarKeyRead(key hostVarKey, literals, identifiers map[string]bool) bool {
	if key.constant != "" && identifiers[key.constant] {
		return true
	}
	if literals[key.value] {
		return true
	}
	for literal := range literals {
		// A reader completing a dotted prefix at run time reads every key under it.
		if strings.HasSuffix(literal, ".") && strings.HasPrefix(key.value, literal) {
			return true
		}
		if key.prefix && strings.HasPrefix(literal, key.value) {
			return true
		}
	}
	return key.prefix && literals[strings.TrimSuffix(key.value, ".")]
}
