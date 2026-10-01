package contract

import (
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
