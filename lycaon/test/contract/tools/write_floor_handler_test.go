package contract

import (
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/ssa/ssautil"
	"reflect"
	"testing"

	"golang.org/x/tools/go/ssa"
)

type writeFloorHandlerFixture struct{}

func (writeFloorHandlerFixture) value()    {}
func (*writeFloorHandlerFixture) pointer() {}

func TestWriteFloorHandlerResolvesValueAndPointerMethods(t *testing.T) {
	fixture := writeFloorHandlerFixture{}
	receiver := reflect.TypeOf(fixture).PkgPath() + ".writeFloorHandlerFixture"
	for _, tc := range []struct {
		name    string
		handler func()
		key     string
	}{
		{"value", fixture.value, "(" + receiver + ").value"},
		{"pointer", fixture.pointer, "(*" + receiver + ").pointer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := &ssa.Function{}
			program := &writeFloorProgram{byName: map[string]*ssa.Function{tc.key: want}}
			got, reason := program.handlerFunction(reflect.ValueOf(tc.handler).Pointer())
			if got != want || reason != "" {
				t.Fatalf("handler=%v reason=%q", got, reason)
			}
		})
	}
}

// Request-field callbacks must come from the current handler's chain; a
// shared replacement primitive does not borrow callbacks from other users.
func TestWriteFloorTracksCallbacksThroughRequestFields(t *testing.T) {
	const source = `package fixture
 func floor() {}
 type request struct { before func() }
 func replace(r request) { r.before() }
 func replacePtr(r *request) { r.before() }
 func reader() { replace(request{before:func(){}}) }
 func writer() { replace(request{before:func(){floor()}}) }
 func readerPtr() { replacePtr(&request{before:func(){}}) }
 func writerPtr() { replacePtr(&request{before:func(){floor()}}) }
 func factory() func() { dependency := func(){floor()}; return func(){dependency()} }
 `
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", source, 0)
	contractcheck.FailErr(t, "parse callback fixture", err)
	pkg, _, err := ssautil.BuildPackage(&types.Config{}, fset, types.NewPackage("fixture", "fixture"), []*ast.File{file}, ssa.SanityCheckFunctions)
	contractcheck.FailErr(t, "build callback fixture", err)
	graph := vta.CallGraph(ssautil.AllFunctions(pkg.Prog), cha.CallGraph(pkg.Prog))
	program := &writeFloorProgram{graph: graph, floor: pkg.Func("floor"), valueCache: map[*ssa.Function]map[*ssa.Function]bool{}}
	for _, tc := range []struct {
		name   string
		writes bool
	}{{"reader", false}, {"writer", true}, {"readerPtr", false}, {"writerPtr", true}} {
		t.Run(tc.name, func(t *testing.T) {
			if chain := program.pathToFloor(pkg.Func(tc.name)); (chain != nil) != tc.writes {
				t.Fatalf("write chain %v, want writer=%v", chain, tc.writes)
			}
		})
	}
	captured := pkg.Func("factory").AnonFuncs
	if chain := program.pathToFloor(captured[len(captured)-1]); chain == nil {
		t.Fatal("registered captured dependency lost its write floor")
	}
}
