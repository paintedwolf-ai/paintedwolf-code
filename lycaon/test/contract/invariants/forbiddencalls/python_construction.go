package forbiddencalls

import (
	"go/ast"
	"go/types"
	"slices"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

var pythonConstruction = []invariants.Invariant{{
	Name: "portable-python-runtime-construction-verifies-the-image",
	Anchors: []invariants.Anchor{
		invariants.Func("internal/pythonruntime", "NewVerifiedRuntime"),
		invariants.Func("internal/portablepython", "NewService"),
	},
	Check: checkPythonRuntimeConstruction,
}, {
	Name:    "portable-python-verification-result-is-construction-local",
	Anchors: []invariants.Anchor{invariants.Func("internal/pythonruntime", "verifyImageInternal")},
	Check:   checkPythonVerificationScope,
}}

// Classify factories by their result type so a new backend is covered without
// registering its constructor name. Function values count as uses as well.
func checkPythonRuntimeConstruction(r *invariants.Run) {
	program := r.Types()
	runtimePkg := program.Packages[r.Go().Module+"/internal/pythonruntime"].Types
	scope := runtimePkg.Scope()
	contract := scope.Lookup("Runtime")
	if contract == nil {
		r.Fatalf("pythonruntime.Runtime is missing")
	}
	iface, ok := contract.Type().Underlying().(*types.Interface)
	if !ok || iface.NumMethods() == 0 {
		r.Fatalf("pythonruntime.Runtime must be a nonempty interface")
	}
	verified := scope.Lookup("NewVerifiedRuntime")
	service := r.Go().Module + "/internal/portablepython"
	uses := 0
	for _, path := range program.Order {
		pkg := program.Packages[path]
		if !slices.Contains(pkg.Types.Imports(), runtimePkg) {
			continue
		}
		for id, obj := range pkg.Info.Uses {
			fn, ok := obj.(*types.Func)
			if !ok || fn.Pkg() != runtimePkg {
				continue
			}
			if fn == verified {
				if path == service {
					uses++
				}
				continue
			}
			results := fn.Signature().Results()
			for i := range results.Len() {
				if types.Implements(results.At(i).Type(), iface) {
					r.ReportAt(id.Pos(), "use pythonruntime.NewVerifiedRuntime to obtain a runtime outside pythonruntime; %s also returns a runtime", fn.Name())
					break
				}
			}
		}
	}
	r.Expect(uses, "verified runtime construction in portablepython")
}

// Keep the verification result internal to construction. Package declarations
// include aliases, stored fields and inferred package variable initializers.
func checkPythonVerificationScope(r *invariants.Run) {
	pkg := r.Types().Packages[r.Go().Module+"/internal/pythonruntime"]
	scope := pkg.Types.Scope()
	proof := scope.Lookup("verifiedImage")
	verify := scope.Lookup("verifyImageInternal")
	if proof == nil {
		r.Fatalf("private verifiedImage type is missing")
	}
	for _, file := range pkg.Files {
		for _, decl := range file.AST.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			region := ast.Node(decl)
			if isFunc {
				// Local variables and private construction helper parameters may
				// carry the result. Only the verifier returns a fresh one.
				if pkg.Info.Defs[fn.Name] == verify {
					continue
				}
				region = fn.Type
				if !fn.Name.IsExported() {
					if fn.Type.Results == nil {
						continue
					}
					region = fn.Type.Results
				}
			}
			ast.Inspect(region, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if ok && (pkg.Info.Uses[id] == proof || pkg.Info.Uses[id] == verify) {
					r.ReportAt(id.Pos(), "keep the verified image local to runtime construction")
				}
				return true
			})
		}
	}
}
