package maintainability

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/scanner"
	"go/token"
	"path"
	"slices"
	"strconv"
	"strings"
)

// typeShape fingerprints declarations and method signatures, excluding bodies and comments.
func typeShape(tree *workingTree, inv *inventory, id string) (string, error) {
	_, name, ok := strings.Cut(id[strings.LastIndex(id, "/")+1:], ".")
	if !ok {
		return "", fmt.Errorf("invalid Go artifact %s", id)
	}
	var declarations []string
	for _, file := range inv.sources[id] {
		raw, err := tree.read(file)
		if err != nil {
			return "", fmt.Errorf("read shape source: %w", err)
		}
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, file, raw, parser.SkipObjectResolution)
		if err != nil {
			return "", fmt.Errorf("parse shape source: %w", err)
		}
		imports := canonicalShapeImports(parsed)
		before := len(declarations)
		for _, declaration := range parsed.Decls {
			switch decl := declaration.(type) {
			case *ast.GenDecl:
				for _, spec := range decl.Specs {
					named, ok := spec.(*ast.TypeSpec)
					if ok && named.Name.Name == name {
						value, err := shapeText(fset, named)
						if err != nil {
							return "", err
						}
						declarations = append(declarations, "type:"+value)
					}
				}
			case *ast.FuncDecl:
				if decl.Recv != nil && receiverName(decl.Recv.List[0].Type) == name {
					value, err := shapeText(fset, decl.Type)
					if err != nil {
						return "", err
					}
					receiver, err := shapeText(fset, decl.Recv.List[0].Type)
					if err != nil {
						return "", err
					}
					declarations = append(declarations, receiver+":"+decl.Name.Name+":"+value)
				}
			}
		}
		if len(declarations) > before && imports != "" {
			declarations = append(declarations, "imports:"+imports)
		}
	}
	if len(declarations) == 0 {
		return "", fmt.Errorf("Go artifact %s has no declarations", id)
	}
	slices.Sort(declarations)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(slices.Compact(declarations), "\n")))), nil
}

func shapeText(fset *token.FileSet, node ast.Node) (string, error) {
	var out bytes.Buffer
	if err := printer.Fprint(&out, fset, node); err != nil {
		return "", fmt.Errorf("format Go shape: %w", err)
	}
	var scan scanner.Scanner
	positions := token.NewFileSet()
	scan.Init(positions.AddFile("", -1, out.Len()), out.Bytes(), nil, 0)
	var tokens []string
	for {
		_, kind, literal := scan.Scan()
		if kind == token.EOF {
			break
		}
		if kind == token.SEMICOLON {
			continue
		}
		tokens = append(tokens, kind.String(), literal)
	}
	raw, err := json.Marshal(tokens)
	return string(raw), err
}

// Import aliases do not change the dependency represented by a type selector.
func canonicalShapeImports(file *ast.File) string {
	imports := map[string]string{}
	var unaliased []string
	for _, spec := range file.Imports {
		source, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := path.Base(source)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if spec.Name == nil || name == "." {
			unaliased = append(unaliased, source)
		}
		imports[name] = fmt.Sprintf("import_%x", sha256.Sum256([]byte(source)))
	}
	ast.Inspect(file, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		qualifier, ok := selector.X.(*ast.Ident)
		if ok && imports[qualifier.Name] != "" {
			qualifier.Name = imports[qualifier.Name]
		}
		return true
	})
	slices.Sort(unaliased)
	return strings.Join(unaliased, "\n")
}
