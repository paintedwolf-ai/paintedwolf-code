package textutil

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/native"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

// A dispatcher's preflight asks the owner which mode a child's argv selects:
// the answer is Prepare's, in-place spellings and script writes included, and
// a script read from a file cannot be classified from argv.
func TestSelectModeNamesWhatPrepareSelects(t *testing.T) {
	owners, err := NewOwners(native.ExecutionRevisions{Boundary: nativefixture.Boundary(t)}, &pinLog{})
	testutil.FailErr(t, "owners", err)
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(t, "memory limits", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(t, "memory", err)
	for source, want := range map[string]string{
		"sort":                          "always",
		"sort -o out":                   "-o",
		"uniq":                          "stdin-input-only",
		"uniq in out":                   "OUTPUT-supplied",
		"iconv -f latin1 -t utf-8 file": "always",
		"iconv -o out file":             "-o",
		"sed s/a/b/":                    "stream-script",
		"sed -i s/a/b/ f":               "-i",
		"sed -Ei s/a/b/ f":              "-i",
		"sed -i.bak s/a/b/ f":           "-i",
		"sed --in-place s/a/b/ f":       "-i",
		"sed w\tout":                    "w-command",
		"sed -f script.sed":             "",
	} {
		var argv commandprogram.Fields
		for _, word := range strings.Split(source, " ") {
			argv = append(argv, commandprogram.Field{Value: strings.ReplaceAll(word, "\t", " ")})
		}
		got, known := owners.SelectMode(t.Context(), memory, argv)
		if want == "" {
			if known {
				t.Errorf("%s: mode %q, want unknown", source, got)
			}
			continue
		}
		if !known || got != want {
			t.Errorf("%s: mode %q (known %v), want %q", source, got, known, want)
		}
	}
}
