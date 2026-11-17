package laws

import (
	"errors"
	"fmt"
	"go/ast"
	"os"
	"strings"
)

// Anchor is something a rule depends on. A missing anchor fails the rule,
// so renaming what a law guards cannot silence it.
type Anchor interface {
	fmt.Stringer
	resolve(*Corpus) error
}

type anchorFunc struct {
	name string
	fn   func(*Corpus) error
}

func (a anchorFunc) String() string { return a.name }

func (a anchorFunc) resolve(c *Corpus) (err error) {
	defer func() {
		if p := recover(); p != nil {
			failure, ok := p.(abort)
			if !ok {
				panic(p)
			}
			err = failure
		}
	}()
	return a.fn(c)
}

// AnchorFunc anchors on a custom lookup, such as a catalog or document key.
// The lookup may read the corpus; a load failure ends the rule.
func AnchorFunc(name string, fn func(*Corpus) error) Anchor {
	return anchorFunc{name: name, fn: fn}
}

// File anchors on a non-empty repository file.
func File(rel string) Anchor {
	return AnchorFunc("file "+rel, func(c *Corpus) error {
		_, err := c.readFile(rel)
		return err
	})
}

// Dir anchors on a repository directory.
func Dir(rel string) Anchor {
	return AnchorFunc("dir "+rel, func(c *Corpus) error {
		info, err := os.Stat(c.Path(rel))
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return errors.New("not a directory")
		}
		return nil
	})
}

// GoPath anchors on a parsed Go file, relative to the module.
func GoPath(rel string) Anchor {
	return AnchorFunc("go file "+rel, func(c *Corpus) error {
		if _, ok := c.Go().File(rel); !ok {
			return errors.New("not in the Go corpus")
		}
		return nil
	})
}

// Package anchors on a module directory holding production Go files.
func Package(dir string) Anchor {
	return AnchorFunc("package "+dir, func(c *Corpus) error {
		for _, f := range c.Go().files {
			if f.Dir == dir && !f.IsTest {
				return nil
			}
		}
		return errors.New("no production files")
	})
}

// Decl anchors on a production declaration in a module directory. Methods,
// struct fields and interface methods are named Type.Member.
func Decl(dir, name string) Anchor {
	return AnchorFunc(dir+"."+name, func(c *Corpus) error {
		g := c.Go()
		if _, ok := g.Decl(g.Pkg(dir), name); !ok {
			return errors.New("not declared")
		}
		return nil
	})
}

// Func anchors on a production function or method with a body.
func Func(dir, name string) Anchor {
	return AnchorFunc("func "+dir+"."+name, func(c *Corpus) error {
		g := c.Go()
		d, ok := g.Decl(g.Pkg(dir), name)
		if !ok {
			return errors.New("not declared")
		}
		if fn, isFunc := d.Node.(*ast.FuncDecl); !isFunc || fn.Body == nil {
			return errors.New("not a function with a body")
		}
		return nil
	})
}

// GoFiles returns the Go files below each module-relative dir that keep
// accepts. Every dir must contribute at least one file.
func (r *Run) GoFiles(keep func(*GoFile) bool, dirs ...string) []*GoFile {
	if len(dirs) == 0 {
		r.Fatalf("Go selection names no directory")
	}
	g := r.Go()
	var out []*GoFile
	for _, dir := range dirs {
		dir = strings.Trim(cleanRel(dir), "/")
		n := 0
		for _, f := range g.files {
			if (dir == "." || f.Dir == dir || strings.HasPrefix(f.Dir, dir+"/") || f.Rel == dir) && keep(f) {
				out = append(out, f)
				n++
			}
		}
		r.Expect(n, "Go files below "+dir)
	}
	return out
}

// Production selects non-test files.
func Production(f *GoFile) bool { return !f.IsTest }

// Tests selects _test.go files.
func Tests(f *GoFile) bool { return f.IsTest }

// AnyGo selects every file.
func AnyGo(*GoFile) bool { return true }

// Source names the module's product source directories.
var Source = []string{"internal", "cmd", "pkg"}

// GoFile returns one parsed Go file by module-relative path.
func (r *Run) GoFile(rel string) *GoFile {
	f, ok := r.Go().File(rel)
	if !ok {
		r.Fatalf("Go file %s is not in the corpus", rel)
	}
	return f
}

// Declaration returns a production declaration or ends the check.
func (r *Run) Declaration(dir, name string) Declaration {
	g := r.Go()
	d, ok := g.Decl(g.Pkg(dir), name)
	if !ok {
		r.Fatalf("%s.%s is not declared", dir, name)
	}
	return d
}

// FuncDecl returns a production function or method with a body.
func (r *Run) FuncDecl(dir, name string) *ast.FuncDecl {
	fn, ok := r.Declaration(dir, name).Node.(*ast.FuncDecl)
	if !ok || fn.Body == nil {
		r.Fatalf("%s.%s is not a function with a body", dir, name)
	}
	return fn
}

// FuncNamed returns the one production function or method named name in a
// module directory, whatever its receiver. None or several ends the check.
func (r *Run) FuncNamed(dir, name string) (*GoFile, *ast.FuncDecl) {
	var file *GoFile
	var found *ast.FuncDecl
	for _, f := range r.Go().files {
		if f.IsTest || f.Dir != dir {
			continue
		}
		for _, decl := range f.AST.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil && fn.Name.Name == name {
				if found != nil {
					r.Fatalf("%s declares %s more than once", dir, name)
				}
				file, found = f, fn
			}
		}
	}
	if found == nil {
		r.Fatalf("%s declares no function %s", dir, name)
	}
	return file, found
}
