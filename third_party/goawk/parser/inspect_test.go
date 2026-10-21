package parser_test

import (
	"reflect"
	"testing"

	"github.com/benhoyt/goawk/parser"
)

func TestConstructsListsRefusableFeaturesInSourceOrder(t *testing.T) {
	src := `BEGIN { system("x") }
/re+/ { print > "f"; printf "%s" | "sort"; "cmd" | getline; getline v < "file" }
function g() { print >> "f" }`
	program, err := parser.ParseProgram([]byte(src), nil)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, c := range program.Constructs() {
		kinds = append(kinds, c.Kind+":"+c.Regex)
		if c.Line < 1 || c.Column < 1 {
			t.Fatalf("construct without a position: %+v", c)
		}
	}
	want := []string{"system:", "regex:re+", "output-file:", "output-pipe:", "input-pipe:", "getline-file:", "output-file:"}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("constructs %q, want %q", kinds, want)
	}
	plain, _ := parser.ParseProgram([]byte(`{ print $1 } END { getline; print }`), nil)
	if got := plain.Constructs(); len(got) != 0 {
		t.Fatalf("plain program constructs %+v", got)
	}
}
