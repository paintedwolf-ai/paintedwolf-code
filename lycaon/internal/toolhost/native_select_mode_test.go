package toolhost

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// boundNatives runs one program and returns the native operations its owners
// bound, with the HTTP owner registered so curl is among them.
func boundNatives(t *testing.T) map[string]command.NativeOperation {
	t.Helper()
	env := newPipeSemanticsEnv(t)
	ownerTools := pipeSemanticsOwnerTools(env.Fixture)
	ownerTools.http = httpaction.Deps{Boundary: env.Runtime.Boundary}
	compose, err := ownerTools.compose(env.Recorder)
	testutil.FailErr(t, "compose program owners", err)
	var natives map[string]command.NativeOperation
	env.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, admission tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		bound, err := compose(tc, paths, memory, admission)
		natives = bound.Natives
		return bound, err
	}
	_, err = env.Call(t, t.Context(), "true")
	testutil.FailErr(t, "run a program", err)
	return natives
}

// Every native command with more than one effect mode has a mode selector, so
// a dispatcher's preflight reviews only what its child will do.
func TestMultiModeNativesNameTheirMode(t *testing.T) {
	natives := boundNatives(t)
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
}

// A dispatcher's preflight asks the owner which mode a child's argv selects:
// the answer is Prepare's, in-place spellings and script writes included. A
// tab in a source stands for a space inside one word.
func TestSelectModeNamesWhatPrepareSelects(t *testing.T) {
	natives := boundNatives(t)
	for source, want := range map[string]string{
		"curl https://example.test/":              "request",
		"curl -o /dev/null https://example.test/": "request",
		"curl -o page.html https://example.test/": "-o--D--c-to-file",

		"tar -tf a.tar":  "listing-or-stream-decode",
		"tar -xf a.tar":  "create-extract-compress-decompress-to-path",
		"unzip -l a.zip": "listing-or-stream-decode",
		"gzip -dc a.gz":  "listing-or-stream-decode",
		"gzip -d a.gz":   "create-extract-compress-decompress-to-path",
		"gunzip":         "listing-or-stream-decode",

		"yq .a f.yaml":      "query",
		"yq -i .a=1 f.yaml": "-i",

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
		// A script read from a file cannot be classified from argv.
		"sed -f script.sed": "",

		"find . -name x -delete": "-delete",
	} {
		var argv commandprogram.Fields
		for _, word := range strings.Split(source, " ") {
			argv = append(argv, commandprogram.Field{Value: strings.ReplaceAll(word, "\t", " ")})
		}
		native := natives[argv[0].Value]
		if native.SelectMode == nil {
			t.Errorf("%s has no mode selector", argv[0].Value)
			continue
		}
		got, known := native.SelectMode(t.Context(), argv)
		if known != (want != "") || got != want {
			t.Errorf("%s: mode %q (known %v), want %q", source, got, known, want)
		}
	}
}
