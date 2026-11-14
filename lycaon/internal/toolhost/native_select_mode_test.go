package toolhost

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// Every native command with more than one effect mode names the mode its
// argv selects, so a dispatcher's preflight reviews only what its child will
// do: env LC_ALL=C sort reads, and xargs sed -Ei edits.
func TestMultiModeNativesNameTheirMode(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	compose := env.Runtime.Owners
	var natives map[string]command.NativeOperation
	env.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, admission tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		bound, err := compose(tc, paths, memory, admission)
		natives = bound.Natives
		return bound, err
	}
	_, err := env.Call(t, t.Context(), "true")
	testutil.FailErr(t, "run a program", err)
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	testutil.FailErr(t, "catalog", err)
	for _, spec := range catalog.Commands {
		// Two-word Git forms resolve through the git umbrella; env is a builtin.
		native, ok := natives[spec.Name]
		if len(spec.Effects) < 2 || !ok {
			continue
		}
		if native.SelectMode == nil {
			t.Errorf("%s has %d effect modes and no mode selector", spec.Name, len(spec.Effects))
		}
	}
	for source, want := range map[string]string{"sort": "always", "sed -Ei s/a/b/ f": "-i", "yq .a f.yaml": "query", "tar -tf a.tar": "listing-or-stream-decode", "find . -name x -delete": "-delete"} {
		var argv commandprogram.Fields
		for _, word := range strings.Fields(source) {
			argv = append(argv, commandprogram.Field{Value: word})
		}
		native := natives[argv[0].Value]
		if native.SelectMode == nil {
			t.Errorf("%s has no mode selector", argv[0].Value)
			continue
		}
		if got, known := native.SelectMode(t.Context(), argv); !known || got != want {
			t.Errorf("%s: mode %q (known %v), want %q", source, got, known, want)
		}
	}
}
