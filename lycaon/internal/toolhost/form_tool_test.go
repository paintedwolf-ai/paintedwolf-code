package toolhost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

func runFormTool(t *testing.T, f *programtest.Fixture, name string, argv func(map[string]any) []string, args map[string]any) command.ProgramOutcome {
	t.Helper()
	tool := &native.FormTool{Programs: f.Runtime, Argv: argv}
	raw, err := f.Invoke(t, t.Context(), name, args, tool.Run)
	testutil.FailErr(t, "invoke "+name, err)
	var outcome command.ProgramOutcome
	testutil.FailErr(t, "decode outcome", json.Unmarshal([]byte(raw), &outcome))
	return outcome
}

// Typed arguments reach the form unchanged, including what a shell would expand.
func TestCodeRewriteToolKeepsPatternsLiteral(t *testing.T) {
	f := newComposedProgramFixture(t)
	path := filepath.Join(f.Dir, "main.go")
	source := "package main\n\nfunc a() string { return wrap(\"it's\") }\n"
	testutil.FailErr(t, "seed source", os.WriteFile(path, []byte(source), 0o644))

	outcome := runFormTool(t, f, "code_rewrite", native.CodeRewriteArgv, map[string]any{
		"path": "main.go", "pattern": "wrap($A)", "rewrite": "unwrap($A)",
	})
	if outcome.State != "completed" || outcome.Status != 0 {
		t.Fatalf("outcome = %+v", outcome)
	}
	got, err := os.ReadFile(path)
	testutil.FailErr(t, "read source", err)
	if !strings.Contains(string(got), `unwrap("it's")`) {
		t.Fatalf("source = %s", got)
	}
}

// The tool and the command form are one route, so they run the same program.
func TestFormToolRunsTheCommandFormsProgram(t *testing.T) {
	f := newComposedProgramFixture(t)
	path := filepath.Join(f.Dir, "main.go")
	testutil.FailErr(t, "seed source", os.WriteFile(path, []byte("package main\n\nvar x = wrap(1)\n"), 0o644))
	runFormTool(t, f, "code_rewrite", native.CodeRewriteArgv, map[string]any{
		"path": "main.go", "pattern": "wrap($A)", "rewrite": "unwrap($A)", "dry_run": true,
	})
	programs := f.Programs(t)
	if len(programs) != 1 || !strings.Contains(programs[0], "code-rewrite") {
		t.Fatalf("programs = %q", programs)
	}
}
