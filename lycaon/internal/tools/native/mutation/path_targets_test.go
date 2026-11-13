package mutation

import (
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
)

// The files argv names are declared targets, where the utility will resolve
// them: tar members under -C, patch names under -d, list files as reads.
func TestArchiveAndPatchOperandsAreDeclaredTargets(t *testing.T) {
	h := newHarness(t, nil)
	for source, want := range map[string][]commandprogram.PathTarget{
		"tar -c -f out.tar -C src -T list.txt -X skip.txt a.go b": {
			{Path: "src"}, {Path: "out.tar", Write: true}, {Path: "list.txt"}, {Path: "skip.txt"}, {Path: "src/a.go"}, {Path: "src/b"},
		},
		"tar -x -f in.tar -C dest":         {{Path: "dest", Write: true}, {Path: "in.tar"}},
		"patch -d sub -i fix.diff main.go": {{Path: "sub/fix.diff"}, {Path: "sub/main.go", Write: true}},
		"patch --dry-run main.go fix.diff": {{Path: "main.go"}, {Path: "fix.diff"}},
		"patch -p1 -i /tmp/abs.diff":       {{Path: "/tmp/abs.diff"}},
	} {
		inv := h.invocation()
		words := strings.Fields(source)
		argv := make(commandprogram.Fields, len(words))
		for i, word := range words {
			argv[i] = commandprogram.Field{Value: word, Span: inv.Span}
		}
		statement := commandsurface.Statement{SyntaxLocation: commandsurface.SyntaxLocation{ID: commandsurface.NodeID(inv.Identity.NodeID), Span: inv.Span}}
		prepared, err := h.owners.Prepare(h.program, commandprogram.OperationRequest{Invocation: inv, OperationID: words[0], Statement: statement, Argv: argv, Environment: h.env})
		if err != nil {
			t.Fatalf("%s: %v", source, err)
		}
		if !slices.Equal(prepared.Review.PathTargets, want) {
			t.Errorf("%s: targets %+v, want %+v", source, prepared.Review.PathTargets, want)
		}
	}
}
