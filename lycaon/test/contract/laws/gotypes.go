package laws

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// Program is the module's production packages type-checked from the parsed
// corpus, for the host platform. Dependencies outside the module come from
// compiler export data.
type Program struct {
	Fset     *token.FileSet
	Packages map[string]*TypedPackage
	// Order lists package paths with each package after its imports.
	Order []string
}

// TypedPackage is one type-checked package.
type TypedPackage struct {
	Path  string
	Types *types.Package
	Info  *types.Info
	Files []*GoFile
}

// Types returns the type-checked program; a failure ends the check.
func (c *Corpus) Types() *Program {
	g := c.Go()
	prog, err := c.typed.get(func() (*Program, error) {
		c.typings.Add(1)
		return typeCheck(g)
	})
	if err != nil {
		panic(abort{fmt.Errorf("type-check Go corpus: %w", err)})
	}
	return prog
}

type listedPackage struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	CgoFiles   []string
	Imports    []string
	Export     string
	Module     *struct{ Main bool }
	Error      *struct{ Err string }
}

func goList(moduleDir string, args ...string) ([]listedPackage, error) {
	cmd := exec.Command("go", append([]string{"list", "-e", "-json=ImportPath,Dir,GoFiles,CgoFiles,Imports,Export,Module,Error"}, args...)...)
	cmd.Dir = moduleDir
	cmd.Env = append(os.Environ(), "PWD="+moduleDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list %s: %w\n%s", strings.Join(args, " "), err, stderr.String())
	}
	var pkgs []listedPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var pkg listedPackage
		if err := dec.Decode(&pkg); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		if pkg.Error != nil {
			return nil, fmt.Errorf("go list %s: %s", pkg.ImportPath, pkg.Error.Err)
		}
		pkgs = append(pkgs, pkg)
	}
	return pkgs, nil
}

func typeCheck(g *GoCorpus) (*Program, error) {
	var patterns []string
	for _, root := range goRoots {
		if root == "test" {
			continue
		}
		if _, err := os.Stat(filepath.Join(g.ModuleDir, root)); err == nil {
			patterns = append(patterns, "./"+root+"/...")
		}
	}
	listed, err := goList(g.ModuleDir, append([]string{"-deps"}, patterns...)...)
	if err != nil {
		return nil, err
	}
	var local []listedPackage
	isLocal := map[string]bool{}
	for _, pkg := range listed {
		if pkg.Module != nil && pkg.Module.Main {
			local = append(local, pkg)
			isLocal[pkg.ImportPath] = true
		}
	}
	if len(local) == 0 {
		return nil, errors.New("go list found no module packages")
	}
	external := map[string]bool{}
	for _, pkg := range local {
		for _, imp := range pkg.Imports {
			if !isLocal[imp] && imp != "C" && imp != "unsafe" {
				external[imp] = true
			}
		}
	}
	exports := map[string]string{}
	if len(external) > 0 {
		deps, err := goList(g.ModuleDir, append([]string{"-deps", "-export"}, sortedKeys(external)...)...)
		if err != nil {
			return nil, err
		}
		for _, pkg := range deps {
			exports[pkg.ImportPath] = pkg.Export
		}
	}

	prog := &Program{Fset: g.Fset, Packages: map[string]*TypedPackage{}}
	imp := &programImporter{
		local: map[string]*types.Package{},
		export: importer.ForCompiler(g.Fset, "gc", func(path string) (io.ReadCloser, error) {
			if exports[path] == "" {
				return nil, fmt.Errorf("no export data for %s", path)
			}
			return os.Open(exports[path])
		}),
	}
	done := map[string]chan struct{}{}
	for _, pkg := range local {
		done[pkg.ImportPath] = make(chan struct{})
		prog.Order = append(prog.Order, pkg.ImportPath)
	}
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	slots := make(chan struct{}, runtime.GOMAXPROCS(0))
	for _, pkg := range local {
		wg.Go(func() {
			defer close(done[pkg.ImportPath])
			for _, dep := range pkg.Imports {
				if ch, ok := done[dep]; ok {
					<-ch
				}
			}
			slots <- struct{}{}
			defer func() { <-slots }()
			typed, err := checkPackage(g, imp, pkg)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			prog.Packages[pkg.ImportPath] = typed
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return prog, nil
}

func checkPackage(g *GoCorpus, imp *programImporter, pkg listedPackage) (*TypedPackage, error) {
	dir, err := filepath.EvalSymlinks(pkg.Dir)
	if err != nil {
		return nil, err
	}
	typed := &TypedPackage{Path: pkg.ImportPath, Info: &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}}
	var files []*ast.File
	for _, name := range append(append([]string(nil), pkg.GoFiles...), pkg.CgoFiles...) {
		f, ok := g.byPath[filepath.Join(dir, name)]
		if !ok {
			return nil, fmt.Errorf("%s/%s is not in the Go corpus", pkg.ImportPath, name)
		}
		typed.Files = append(typed.Files, f)
		files = append(files, f.AST)
	}
	var typeErrs []string
	conf := types.Config{
		Importer:    imp,
		FakeImportC: len(pkg.CgoFiles) > 0,
		Error:       func(err error) { typeErrs = append(typeErrs, err.Error()) },
	}
	checked, _ := conf.Check(pkg.ImportPath, g.Fset, files, typed.Info)
	// FakeImportC leaves C values untyped; cgo packages report follow-on
	// errors while their Go API still type-checks for importers.
	if len(typeErrs) > 0 && len(pkg.CgoFiles) == 0 {
		sort.Strings(typeErrs)
		return nil, fmt.Errorf("type-check %s:\n  %s", pkg.ImportPath, strings.Join(typeErrs[:min(len(typeErrs), 5)], "\n  "))
	}
	typed.Types = checked
	imp.add(pkg.ImportPath, checked)
	return typed, nil
}

type programImporter struct {
	mu     sync.Mutex
	local  map[string]*types.Package
	export types.Importer
}

func (i *programImporter) add(path string, pkg *types.Package) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.local[path] = pkg
}

func (i *programImporter) Import(path string) (*types.Package, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if pkg, ok := i.local[path]; ok {
		return pkg, nil
	}
	return i.export.Import(path)
}

// Package returns a typed package by module-relative directory.
func (r *Run) TypedPackage(dir string) *TypedPackage {
	prog := r.Types()
	pkg, ok := prog.Packages[r.Go().Pkg(dir)]
	if !ok {
		r.Fatalf("package %s is not in the typed program", dir)
	}
	return pkg
}

// Callee returns the function or method a call statically invokes, or nil.
func (p *TypedPackage) Callee(call *ast.CallExpr) *types.Func {
	fun := ast.Unparen(call.Fun)
	if index, ok := fun.(*ast.IndexExpr); ok {
		fun = index.X
	}
	if index, ok := fun.(*ast.IndexListExpr); ok {
		fun = index.X
	}
	switch f := fun.(type) {
	case *ast.Ident:
		fn, _ := p.Info.Uses[f].(*types.Func)
		return fn
	case *ast.SelectorExpr:
		if sel, ok := p.Info.Selections[f]; ok {
			fn, _ := sel.Obj().(*types.Func)
			return fn
		}
		fn, _ := p.Info.Uses[f.Sel].(*types.Func)
		return fn
	}
	return nil
}
