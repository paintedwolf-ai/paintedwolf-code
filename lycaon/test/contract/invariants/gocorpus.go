package invariants

import (
	"crypto/sha256"
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
	"sync/atomic"
	"weak"

	"golang.org/x/mod/modfile"
)

// goRoots are the module directories parsed into the Go corpus.
var goRoots = []string{"internal", "cmd", "pkg", "config", "test"}

type funcSpan struct {
	name    string
	hasBody bool
	start   int
	end     int
}

type declKind uint8

const (
	declOther declKind = iota
	declFunc
	declType
	declValue
	declMember
)

type declEntry struct {
	file        *GoFile
	kind        declKind
	hasBody     bool
	constStr    string
	hasConstStr bool
}

type indexedDecl struct {
	name        string
	kind        declKind
	hasBody     bool
	constStr    string
	hasConstStr bool
}

// GoFile is one indexed source file. Rel and Dir are relative to the module.
// Full syntax trees and source bytes are loaded on demand so the shared corpus
// retains only compact metadata, imports, and declaration/function indexes.
type GoFile struct {
	Rel     string
	Dir     string
	Path    string
	Pkg     string
	PkgName string
	IsTest  bool

	corpus *GoCorpus
	// imports maps each local import name to its path.
	imports map[string]string
	// allImports lists every imported path, including blank and dot imports.
	allImports []string
	// dots lists dot-imported paths.
	dots []string
	// funcs holds compact top-level function and method spans (byte offsets).
	funcs []funcSpan
	// sha256 records the content digest observed during initial indexing so
	// on-demand reads fail closed if the file changes on disk.
	sha256 [sha256.Size]byte

	mu             sync.Mutex
	tokenFile      *token.File
	cachedAST      weak.Pointer[ast.File]
	onDemandParses atomic.Int64
}

// GoCorpus is every Go file below goRoots, indexed once with comments validated.
// Full syntax trees are parsed on demand per file and remain eligible for
// garbage collection once callers stop referencing them.
type GoCorpus struct {
	Fset   *token.FileSet
	Module string
	// ModuleDir is the absolute module directory.
	ModuleDir string
	files     []*GoFile
	byRel     map[string]*GoFile
	byPath    map[string]*GoFile
	decls     map[string]declEntry
}

// Declaration is one top-level declaration, a method, or a struct field.
type Declaration struct {
	File *GoFile
	Node ast.Node
	Pos  token.Pos
}

// Go returns the indexed Go corpus; a load or parse failure ends the check.
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

	g := &GoCorpus{
		Fset:      token.NewFileSet(),
		Module:    module,
		ModuleDir: moduleDir,
		files:     make([]*GoFile, len(paths)),
		byRel:     make(map[string]*GoFile, len(paths)),
		byPath:    make(map[string]*GoFile, len(paths)),
		decls:     map[string]declEntry{},
	}
	fileDecls := make([][]indexedDecl, len(paths))
	errs := make([]error, len(paths))
	var wg sync.WaitGroup
	next := make(chan int)
	for range runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			for i := range next {
				g.files[i], fileDecls[i], errs[i] = g.indexFile(paths[i])
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
	for i, f := range g.files {
		g.byRel[f.Rel] = f
		g.byPath[f.Path] = f
		if !f.IsTest {
			for _, d := range fileDecls[i] {
				key := f.Pkg + "." + d.name
				if _, dup := g.decls[key]; !dup {
					g.decls[key] = declEntry{
						file:        f,
						kind:        d.kind,
						hasBody:     d.hasBody,
						constStr:    d.constStr,
						hasConstStr: d.hasConstStr,
					}
				}
			}
		}
	}
	return g, nil
}

func (g *GoCorpus) indexFile(abs string) (*GoFile, []indexedDecl, error) {
	src, err := os.ReadFile(abs)
	if err != nil {
		return nil, nil, err
	}
	tmpFset := token.NewFileSet()
	file, err := parser.ParseFile(tmpFset, abs, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, err
	}
	rel, err := filepath.Rel(g.ModuleDir, abs)
	if err != nil {
		return nil, nil, err
	}
	rel = filepath.ToSlash(rel)
	f := &GoFile{
		Rel:     rel,
		Dir:     path.Dir(rel),
		Path:    abs,
		Pkg:     g.Module + "/" + path.Dir(rel),
		PkgName: file.Name.Name,
		IsTest:  strings.HasSuffix(rel, "_test.go"),
		corpus:  g,
		imports: make(map[string]string, len(file.Imports)),
		sha256:  sha256.Sum256(src),
	}
	if len(file.Imports) > 0 {
		f.allImports = make([]string, 0, len(file.Imports))
	}
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, nil, err
		}
		if !slices.Contains(f.allImports, importPath) {
			f.allImports = append(f.allImports, importPath)
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

	tf := tmpFset.File(file.Pos())
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			f.funcs = append(f.funcs, funcSpan{
				name:    fn.Name.Name,
				hasBody: fn.Body != nil,
				start:   tf.Offset(fn.Pos()),
				end:     tf.Offset(fn.End()),
			})
		}
	}

	var decls []indexedDecl
	if !f.IsTest {
		decls = extractDecls(file)
	}
	return f, decls, nil
}

func extractDecls(file *ast.File) []indexedDecl {
	var out []indexedDecl
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if recv := ReceiverType(d); recv != "" {
				name = recv + "." + name
			}
			out = append(out, indexedDecl{name: name, kind: declFunc, hasBody: d.Body != nil})
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					out = append(out, indexedDecl{name: s.Name.Name, kind: declType})
					indexMembers(s, func(member string, _ token.Pos) {
						out = append(out, indexedDecl{name: s.Name.Name + "." + member, kind: declMember})
					})
				case *ast.ValueSpec:
					for i, name := range s.Names {
						var constStr string
						var hasConstStr bool
						if i < len(s.Values) {
							constStr, hasConstStr = StringLit(s.Values[i])
						}
						out = append(out, indexedDecl{
							name:        name.Name,
							kind:        declValue,
							constStr:    constStr,
							hasConstStr: hasConstStr,
						})
					}
				}
			}
		}
	}
	return out
}

func findDeclInAST(file *ast.File, want string) (ast.Node, token.Pos, bool) {
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if recv := ReceiverType(d); recv != "" {
				name = recv + "." + name
			}
			if name == want {
				return d, d.Name.Pos(), true
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name.Name == want {
						return s, s.Name.Pos(), true
					}
					var foundPos token.Pos
					var found bool
					indexMembers(s, func(member string, pos token.Pos) {
						if !found && s.Name.Name+"."+member == want {
							foundPos = pos
							found = true
						}
					})
					if found {
						return s, foundPos, true
					}
				case *ast.ValueSpec:
					for _, name := range s.Names {
						if name.Name == want {
							return s, name.Pos(), true
						}
					}
				}
			}
		}
	}
	return nil, token.NoPos, false
}

func (f *GoFile) readVerifiedSource() ([]byte, error) {
	src, err := os.ReadFile(f.Path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", f.Rel, err)
	}
	if sha256.Sum256(src) != f.sha256 {
		return nil, fmt.Errorf("%s changed after Go corpus index was built", f.Rel)
	}
	return src, nil
}

// Syntax parses and returns f's syntax tree on demand, reusing a live tree
// while it remains reachable without pinning it across garbage collections.
// Each GoFile registers at most one token.File in GoCorpus.Fset; re-parses
// after eviction align a temporary FileSet to that base so positions remain
// identical without growing GoCorpus.Fset.
func (f *GoFile) Syntax() *ast.File {
	f.mu.Lock()
	defer f.mu.Unlock()
	if file := f.cachedAST.Value(); file != nil {
		return file
	}
	src, err := f.readVerifiedSource()
	if err != nil {
		panic(abort{err})
	}
	var file *ast.File
	if f.tokenFile == nil {
		file, err = parser.ParseFile(f.corpus.Fset, f.Path, src, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			panic(abort{fmt.Errorf("parse %s: %w", f.Rel, err)})
		}
		f.tokenFile = f.corpus.Fset.File(file.Pos())
	} else {
		tmpFset := token.NewFileSet()
		if base := f.tokenFile.Base(); base > 1 {
			tmpFset.AddFile("", 1, base-2)
		}
		file, err = parser.ParseFile(tmpFset, f.Path, src, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			panic(abort{fmt.Errorf("parse %s: %w", f.Rel, err)})
		}
		if gotBase := tmpFset.File(file.Pos()).Base(); gotBase != f.tokenFile.Base() {
			panic(abort{fmt.Errorf("re-parse %s: position base %d != %d", f.Rel, gotBase, f.tokenFile.Base())})
		}
	}
	f.onDemandParses.Add(1)
	f.cachedAST = weak.Make(file)
	return file
}

// Bytes reads f's raw source bytes on demand without retaining them in the corpus.
func (f *GoFile) Bytes() []byte {
	src, err := f.readVerifiedSource()
	if err != nil {
		panic(abort{err})
	}
	return src
}

func (f *GoFile) hasFuncName(name string) bool {
	for _, fn := range f.funcs {
		if fn.name == name {
			return true
		}
	}
	return false
}

func (f *GoFile) hasFuncBody(name string) bool {
	for _, fn := range f.funcs {
		if fn.name == name && fn.hasBody {
			return true
		}
	}
	return false
}

func (f *GoFile) hasAnyFuncBody() bool {
	for _, fn := range f.funcs {
		if fn.hasBody {
			return true
		}
	}
	return false
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

// File returns the file at a module-relative path, if indexed.
func (g *GoCorpus) File(rel string) (*GoFile, bool) {
	f, ok := g.byRel[cleanRel(rel)]
	return f, ok
}

// HasDecl reports whether a production declaration exists without parsing its AST.
func (g *GoCorpus) HasDecl(pkg, name string) bool {
	_, ok := g.decls[pkg+"."+name]
	return ok
}

// HasFunc reports whether a production function or method with a body exists
// without parsing its AST.
func (g *GoCorpus) HasFunc(pkg, name string) bool {
	d, ok := g.decls[pkg+"."+name]
	return ok && d.kind == declFunc && d.hasBody
}

// Decl looks up a production declaration by import path and name, parsing only
// the declaring file on demand. Methods and fields are named Type.Member.
func (g *GoCorpus) Decl(pkg, name string) (Declaration, bool) {
	entry, ok := g.decls[pkg+"."+name]
	if !ok {
		return Declaration{}, false
	}
	node, pos, found := findDeclInAST(entry.file.Syntax(), name)
	if !found {
		return Declaration{}, false
	}
	return Declaration{File: entry.file, Node: node, Pos: pos}, true
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
	return slices.Contains(f.allImports, importPath)
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

// Files returns every indexed file.
func (g *GoCorpus) Files() []*GoFile { return g.files }

// Line returns the 1-based line of pos.
func (g *GoCorpus) Line(pos token.Pos) int { return g.Fset.Position(pos).Line }

// Source returns the source text of node.
func (f *GoFile) Source(g *GoCorpus, node ast.Node) string {
	tf := g.Fset.File(node.Pos())
	src := f.Bytes()
	return string(src[tf.Offset(node.Pos()):tf.Offset(node.End())])
}
