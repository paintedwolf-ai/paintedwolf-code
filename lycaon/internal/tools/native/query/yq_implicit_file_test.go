package query

import (
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

// yq's file-or-expression decision runs before admission: a bare operand
// inside the roots is a file only when it is one there, and an operand that
// resolves beyond them is a file for admission to review, not inspected.
func TestYqImplicitFileDecisionStaysInsideAuthority(t *testing.T) {
	h := newHarness(t, map[string]string{"config.yaml": "a: 1\n"})
	for words, want := range map[string][]commandprogram.PathTarget{
		"config.yaml":               {{Path: "config.yaml"}},
		".a":                        nil,
		"sub/../../outside.yaml .a": {{Path: "sub/../../outside.yaml"}},
	} {
		argv := commandprogram.Fields{{Value: "yq"}}
		for _, word := range strings.Fields(words) {
			argv = append(argv, commandprogram.Field{Value: word})
		}
		bound, err := (commandprogram.CatalogBinder{}).Bind(h.owners.specs["yq"], argv)
		if err != nil {
			t.Fatalf("%s: bind: %v", words, err)
		}
		got := queryPathTargets(bound, h.program.Paths)
		if len(got) == 0 {
			got = nil
		}
		if !slices.Equal(got, want) {
			t.Errorf("yq %s: targets %+v, want %+v", words, got, want)
		}
	}
}
