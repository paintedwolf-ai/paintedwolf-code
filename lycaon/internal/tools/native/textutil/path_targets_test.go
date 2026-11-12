package textutil

import (
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// Every utility that opens the files its operands name declares them, so
// admission reviews each read and write before the utility runs.
func TestFileOperandsAreDeclaredTargets(t *testing.T) {
	h := newHarness(t)
	for source, want := range map[string][]commandprogram.PathTarget{
		"cut -d , -f 1 a.csv b.csv":                  {{Path: "a.csv"}, {Path: "b.csv"}},
		"column -t table.txt":                        {{Path: "table.txt"}},
		"fold -w 20 long.txt":                        {{Path: "long.txt"}},
		"rev lines.txt":                              {{Path: "lines.txt"}},
		"iconv -f latin1 -t utf-8 -o out.txt in.txt": {{Path: "out.txt", Write: true}, {Path: "in.txt"}},
		"sort -o sorted.txt raw.txt":                 {{Path: "sorted.txt", Write: true}, {Path: "raw.txt"}},
	} {
		words := strings.Fields(source)
		argv := make(commandprogram.Fields, len(words))
		for i, word := range words {
			argv[i] = commandprogram.Field{Value: word}
		}
		request := commandprogramRequest("targets")
		request.OperationID, request.Argv = words[0], argv
		request.Invocation.Kind = toolcontract.ProgramChildInvocation
		request.Statement.Span = request.Invocation.Span
		prepared, err := h.owners.Prepare(t.Context(), request)
		if err != nil {
			t.Fatalf("%s: %v", source, err)
		}
		if !slices.Equal(prepared.Review.PathTargets, want) {
			t.Errorf("%s: targets %+v, want %+v", source, prepared.Review.PathTargets, want)
		}
		prepared.Close()
	}
}
