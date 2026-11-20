package invariants

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/mod/modfile"
)

// goRoots are the module directories parsed into the Go corpus.
var goRoots = []string{"internal", "cmd", "pkg", "config", "test"}

// GoFile is one parsed source file. Rel and Dir are relative to the module.
type GoFile struct {
	Rel    string
	Dir    string
	Path   string
	Pkg    string
	IsTest bool
	AST    *ast.File
	Src    []byte
	// imports maps each local import name to its path.
	imports map[string]string
	// dots lists dot-imported paths.
	dots []string
}

// GoCorpus is every Go file below goRoots, parsed once with comments.
type GoCorpus struct {
	Fset   *token.FileSet
	Module string
	// ModuleDir is the absolute module directory.
	ModuleDir string
	files     []*GoFile
	byRel     map[string]*GoFile
	byPath    map[string]*GoFile
	decls     map[string]Declaration
}

// Declaration is one top-level declaration, a method, or a struct field.
type Declaration struct {
	File *GoFile
	Node ast.Node
	Pos  token.Pos
}

// Go returns the parsed Go corpus; a load or parse failure ends the check.
func (c *Corpus) Go() *GoCorpus {
	c.goOnce.Do(func() {
		c.parses.Add(1)
		c.goSrc, c.goErr = parseGo(filepath.Join(c.root, "lycaon"))
	})
	if c.goErr != nil {
		panic(abort{fmt.Errorf("load Go corpus: %w", c.goErr)})
	}
	return c.goSrc
}

func parseGo(moduleDir string) (*GoCorpus, error) {
	// The go command reports canonical directories; key files the same way.
	moduleDir, err := filepath.EvalSymlinks(moduleDir)
	if err != nil {
		return nil, err
	}
	modData, err := os.ReadFile(filepath.Join(moduleDir, "go.mod"))
	if err != nil {
		return nil, err
	}
	module := modfile.ModulePath(modData)
	if module == "" {
		return nil, errors.New("go.mod declares no module")
	}
	var paths []string
	for _, root := range goRoots {
		dir := filepath.Join(moduleDir, root)
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			continue
		}
		err := filepath.WalkDir(dir, func(p string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := entry.Name()
			if entry.IsDir() {
				if p != dir && (name == "testdata" || name == "vendor" || name == "node_modules" ||
					strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(name, ".go") {
				if !entry.Type().IsRegular() {
					return fmt.Errorf("%s is not a regular file", p)
				}
				paths = append(paths, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no Go files below %s", moduleDir)
	}
	sort.Strings(paths)

	g := &GoCorpus{Fset: token.NewFileSet(), Module: module, ModuleDir: moduleDir,
		files: make([]*GoFile, len(paths)), byRel: map[string]*GoFile{}, byPath: map[string]*GoFile{},
		decls: map[string]Declaration{}}
	errs := make([]error, len(paths))
	var wg sync.WaitGroup
	next := make(chan int)
	for range runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			for i := range next {
				g.files[i], errs[i] = g.parseFile(paths[i])
			}
		})
	}
	for i := range paths {
		next <- i
	}
	close(next)
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	for _, f := range g.files {
		g.byRel[f.Rel] = f
		g.byPath[f.Path] = f
		if !f.IsTest {
			g.indexDecls(f)
		}
	}
	return g, nil
}

func (g *GoCorpus) parseFile(abs string) (*GoFile, error) {
	src, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	file, err := parser.ParseFile(g.Fset, abs, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(g.ModuleDir, abs)
	if err != nil {
		return nil, err
	}
	rel = filepath.ToSlash(rel)
	f := &GoFile{Rel: rel, Dir: path.Dir(rel), Path: abs, Pkg: g.Module + "/" + path.Dir(rel),
		IsTest: strings.HasSuffix(rel, "_test.go"), AST: file, Src: src, imports: map[string]string{}}
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, err
		}
		name := path.Base(importPath)
		if spec.Name != nil {
			name = spec.Name.Name
		} else if base := path.Base(importPath); len(base) > 1 && base[0] == 'v' && isDigits(base[1:]) {
			name = path.Base(path.Dir(importPath))
		}
		switch name {
		case ".":
			f.dots = append(f.dots, importPath)
		case "_":
		default:
			f.imports[name] = importPath
		}
	}
	return f, nil
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// Pkg turns a module-relative directory into its import path.
func (g *GoCorpus) Pkg(dir string) string { return g.Module + "/" + strings.Trim(dir, "/") }

// Position renders pos with a module-relative file name.
func (g *GoCorpus) Position(pos token.Pos) string {
	p := g.Fset.Position(pos)
	if rel, err := filepath.Rel(g.ModuleDir, p.Filename); err == nil {
		p.Filename = filepath.ToSlash(rel)
	}
	return fmt.Sprintf("%s:%d", p.Filename, p.Line)
}

// File returns the file at a module-relative path, if parsed.
func (g *GoCorpus) File(rel string) (*GoFile, bool) {
	f, ok := g.byRel[cleanRel(rel)]
	return f, ok
}

// Decl looks up a production declaration by import path and name. Methods
// and fields are named Type.Member.
func (g *GoCorpus) Decl(pkg, name string) (Declaration, bool) {
	d, ok := g.decls[pkg+"."+name]
	return d, ok
}

func (g *GoCorpus) indexDecls(f *GoFile) {
	add := func(name string, node ast.Node, pos token.Pos) {
		key := f.Pkg + "." + name
		if _, dup := g.decls[key]; !dup {
			g.decls[key] = Declaration{File: f, Node: node, Pos: pos}
		}
	}
	for _, decl := range f.AST.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if recv := ReceiverType(d); recv != "" {
				add(recv+"."+d.Name.Name, d, d.Name.Pos())
			} else {
				add(d.Name.Name, d, d.Name.Pos())
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					add(s.Name.Name, s, s.Name.Pos())
					indexMembers(s, func(member string, pos token.Pos) { add(s.Name.Name+"."+member, s, pos) })
				case *ast.ValueSpec:
					for _, name := range s.Names {
						add(name.Name, s, name.Pos())
					}
				}
			}
		}
	}
}

func indexMembers(s *ast.TypeSpec, add func(string, token.Pos)) {
	var fields *ast.FieldList
	switch t := s.Type.(type) {
	case *ast.StructType:
		fields = t.Fields
	case *ast.InterfaceType:
		fields = t.Methods
	}
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		for _, name := range field.Names {
			add(name.Name, name.Pos())
		}
		if len(field.Names) == 0 {
			if name := typeName(field.Type); name != "" {
				add(name, field.Pos())
			}
		}
	}
}

// ReceiverType returns a method's receiver type name without pointer or
// type parameters, or "" for a function.
func ReceiverType(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	return typeName(fn.Recv.List[0].Type)
}

// TypeName returns the bare name of a type expression: T, *T, pkg.T, T[P].
func TypeName(expr ast.Expr) string { return typeName(expr) }

func typeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return typeName(t.X)
	case *ast.IndexExpr:
		return typeName(t.X)
	case *ast.IndexListExpr:
		return typeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

// ImportPath returns the path a local package name refers to in f.
func (f *GoFile) ImportPath(name string) (string, bool) {
	p, ok := f.imports[name]
	return p, ok
}

// Imports reports whether f imports path under any name, blank included.
func (f *GoFile) Imports(importPath string) bool {
	for _, spec := range f.AST.Imports {
		if p, err := strconv.Unquote(spec.Path.Value); err == nil && p == importPath {
			return true
		}
	}
	return false
}

// DotImports reports whether f imports path with a dot.
func (f *GoFile) DotImports(importPath string) bool {
	return slices.Contains(f.dots, importPath)
}

// Qualified resolves pkg.Name through f's imports: it returns the import
// path and selected name for a package-qualified selector.
func (f *GoFile) Qualified(expr ast.Expr) (pkg, name string, ok bool) {
	sel, isSel := expr.(*ast.SelectorExpr)
	if !isSel {
		return "", "", false
	}
	id, isIdent := sel.X.(*ast.Ident)
	if !isIdent {
		return "", "", false
	}
	p, imported := f.imports[id.Name]
	if !imported {
		return "", "", false
	}
	return p, sel.Sel.Name, true
}

// Files returns every parsed file.
func (g *GoCorpus) Files() []*GoFile { return g.files }

// Line returns the 1-based line of pos.
func (g *GoCorpus) Line(pos token.Pos) int { return g.Fset.Position(pos).Line }

// Source returns the source text of node.
func (f *GoFile) Source(g *GoCorpus, node ast.Node) string {
	tf := g.Fset.File(node.Pos())
	return string(f.Src[tf.Offset(node.Pos()):tf.Offset(node.End())])
}
