package analyzers

import (
	"go/ast"
	"go/types"
	"regexp"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// SelfEmbed reports a struct with methods that embeds a pointer to another
// struct of its own package with methods: one receiver's methods split across
// types.
var SelfEmbed = &analysis.Analyzer{
	Name: "selfembed",
	Doc:  "report structs with methods embedding a pointer to a same-package struct with methods",
	Run:  runSelfEmbed,
}

func runSelfEmbed(pass *analysis.Pass) (any, error) {
	for _, file := range productionFiles(pass) {
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := spec.Type.(*ast.StructType)
			if !ok {
				return false
			}
			self, ok := pass.TypesInfo.Defs[spec.Name].(*types.TypeName)
			if !ok || !hasOwnMethods(self.Type()) {
				return false
			}
			for _, field := range st.Fields.List {
				if _, isPtr := field.Type.(*ast.StarExpr); len(field.Names) > 0 || !isPtr {
					continue
				}
				named := namedOf(pass.TypesInfo.TypeOf(field.Type))
				if named == nil || named.Obj().Pkg() != pass.Pkg {
					continue
				}
				if _, isStruct := named.Underlying().(*types.Struct); !isStruct {
					continue
				}
				if hasOwnMethods(named) {
					pass.Reportf(field.Pos(), "%s embeds %s from its own package", spec.Name.Name, named.Obj().Name())
				}
			}
			return false
		})
	}
	return nil, nil
}

// CopyDrop reports a struct literal built from several fields of one value of
// a same-named type, such as a twin in another package, that omits a field
// both types share with the same type.
var CopyDrop = &analysis.Analyzer{
	Name: "copydrop",
	Doc:  "report struct literals copying from one value that drop a shared field",
	Run:  runCopyDrop,
}

func runCopyDrop(pass *analysis.Pass) (any, error) {
	for _, file := range productionFiles(pass) {
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			target := namedOf(pass.TypesInfo.TypeOf(lit))
			if target == nil {
				return true
			}
			targetStruct, ok := target.Underlying().(*types.Struct)
			if !ok {
				return true
			}
			keys := map[string]bool{}
			sources := map[*types.Var]int{}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					return true
				}
				if key, ok := kv.Key.(*ast.Ident); ok {
					keys[key.Name] = true
				}
				if sel, ok := kv.Value.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok {
						if v, ok := pass.TypesInfo.Uses[id].(*types.Var); ok {
							sources[v]++
						}
					}
				}
			}
			for v, copied := range sources {
				if copied < 2 {
					continue
				}
				source := namedOf(v.Type())
				if source == nil || source.Obj().Name() != target.Obj().Name() {
					continue
				}
				sourceStruct, ok := source.Underlying().(*types.Struct)
				if !ok {
					continue
				}
				var shared, omitted []string
				for i := 0; i < targetStruct.NumFields(); i++ {
					field := targetStruct.Field(i)
					if field.Embedded() || !field.Exported() {
						continue
					}
					if other := fieldNamed(sourceStruct, field.Name()); other != nil && types.Identical(other.Type(), field.Type()) {
						shared = append(shared, field.Name())
						if !keys[field.Name()] {
							omitted = append(omitted, field.Name())
						}
					}
				}
				// A literal that sets most shared fields and drops a few loses
				// values; one that sets a few seeds a new value on purpose.
				if len(omitted) > 0 && 2*(len(shared)-len(omitted)) > len(shared) {
					pass.Reportf(lit.Pos(), "%s built from %s (%s) omits shared %s",
						target.Obj().Name(), v.Name(), source.Obj().Name(), fieldList(omitted))
				}
			}
			return true
		})
	}
	return nil, nil
}

// TestImport reports a production package importing a test-support package.
var TestImport = &analysis.Analyzer{
	Name: "testimport",
	Doc:  "report production packages importing test-support packages",
	Run:  runTestImport,
}

var testSupport stringList

func init() {
	TestImport.Flags.Var(&testSupport, "test-support", "comma-separated patterns for test-support package paths")
}

func runTestImport(pass *analysis.Pass) (any, error) {
	patterns, err := testSupport.compile()
	if err != nil {
		return nil, err
	}
	if matchesAny(patterns, pass.Pkg.Path()) {
		return nil, nil
	}
	for _, file := range productionFiles(pass) {
		for _, spec := range file.Imports {
			path := strings.Trim(spec.Path.Value, `"`)
			if matchesAny(patterns, path) {
				pass.Reportf(spec.Pos(), "production package %s imports test-support package %s", pass.Pkg.Path(), path)
			}
		}
	}
	return nil, nil
}

func fieldList(names []string) string {
	if len(names) == 1 {
		return "field " + names[0]
	}
	return "fields " + strings.Join(names, ", ")
}

// hasOwnMethods reports methods declared on the named type itself.
func hasOwnMethods(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	return ok && named.NumMethods() > 0
}

func namedOf(t types.Type) *types.Named {
	if t == nil {
		return nil
	}
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, _ := types.Unalias(t).(*types.Named)
	return named
}

func fieldNamed(st *types.Struct, name string) *types.Var {
	for i := 0; i < st.NumFields(); i++ {
		if st.Field(i).Name() == name {
			return st.Field(i)
		}
	}
	return nil
}

// productionFiles are the package's hand-written non-test files.
func productionFiles(pass *analysis.Pass) []*ast.File {
	var out []*ast.File
	for _, file := range pass.Files {
		name := pass.Fset.File(file.Pos()).Name()
		if strings.HasSuffix(name, "_test.go") || ast.IsGenerated(file) {
			continue
		}
		out = append(out, file)
	}
	return out
}

type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(s string) error {
	*l = nil
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*l = append(*l, part)
		}
	}
	return nil
}

func (l stringList) compile() ([]*regexp.Regexp, error) {
	var out []*regexp.Regexp
	for _, p := range l {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, err
		}
		out = append(out, re)
	}
	return out, nil
}

func matchesAny(patterns []*regexp.Regexp, s string) bool {
	for _, re := range patterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// All is every per-package analyzer.
var All = []*analysis.Analyzer{Reexport, SelfEmbed, CopyDrop, TestImport}
