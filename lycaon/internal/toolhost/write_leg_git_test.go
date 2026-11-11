package toolhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/pkg/api"
)

type writeLegWorkspace struct{}

func (writeLegWorkspace) ValidateMeta(context.Context) error { return nil }

func (writeLegWorkspace) EnsureParents(context.Context, string) error { return nil }

// The production owner composition runs the shell probe a model writes.
func TestACOP02RevParseProbeFallsThroughToPwdOnAWriteLeg(t *testing.T) {
	f := newComposedProgramFixture(t)
	out := &tools.ToolInvocationOut{}
	configure := f.Configure
	f.Configure = func(tc *tools.ToolContext) {
		if configure != nil {
			configure(tc)
		}
		tc.Effects.Out = out
		tc.Source.SourceWorkspaceKind, tc.Identity.WorkerJobID = api.SourceWorkspaceKindWorker, "job-1"
		tc.Source.WorkerBranchRoot, tc.Source.BranchWorkspace = f.Dir, writeLegWorkspace{}
	}
	outcome := callComposedProgram(t, f, `git rev-parse --show-toplevel 2>/dev/null || pwd`)
	if outcome.Refusal != nil || outcome.Status != 0 || outcome.Stderr != "" {
		t.Fatalf("probe outcome: %+v refusal %+v", outcome, outcome.Refusal)
	}
	want, err := filepath.EvalSymlinks(f.Dir)
	testutil.FailErr(t, "resolve workspace", err)
	got, err := filepath.EvalSymlinks(strings.TrimSpace(outcome.Stdout))
	testutil.FailErr(t, "resolve printed directory", err)
	if got != want {
		t.Fatalf("probe printed %q, want %q", got, want)
	}
	if !out.Facts.HasCode(toolrejection.GitNoRepositoryOnWriteLegCode) {
		t.Fatalf("facts = %+v, want the warning's fact", out.Facts)
	}
}

func TestWorkerBranchPinnedMutationsApplyAcrossCommandAndFormTool(t *testing.T) {
	f := newComposedProgramFixture(t)
	configure := f.Configure
	f.Configure = func(tc *tools.ToolContext) {
		if configure != nil {
			configure(tc)
		}
		tc.Source.SourceWorkspaceKind, tc.Identity.WorkerJobID = api.SourceWorkspaceKindWorker, "job-1"
		tc.Source.WorkerBranchRoot, tc.Source.BranchWorkspace = f.Dir, writeLegWorkspace{}
	}
	path := filepath.Join(f.Dir, "main.go")
	testutil.FailErr(t, "seed main.go", os.WriteFile(path, []byte("package main\n\nfunc run() string { return wrap(\"alpha\") }\n"), 0o644))

	outcome := runFormTool(t, f, "code_rewrite", native.CodeRewriteArgv, map[string]any{
		"path": "main.go", "pattern": "wrap($A)", "rewrite": "unwrap($A)",
	})
	if outcome.State != "completed" || outcome.Status != 0 {
		t.Fatalf("code_rewrite outcome = %+v", outcome)
	}
	outcome = callComposedProgram(t, f, `
edit --path main.go --old-string 'unwrap("alpha")' --new-string 'unwrap("beta")'
replace-lines --path main.go --start 3 --end 3 --content 'func run() string { return unwrap("gamma") }'
printf 'package main\n\nfunc run() string { return unwrap("delta") }\n' > main.go
`)
	if outcome.State != "completed" || outcome.Status != 0 {
		t.Fatalf("command mutations outcome = %+v", outcome)
	}
	got, err := os.ReadFile(path)
	testutil.FailErr(t, "read main.go", err)
	if !strings.Contains(string(got), `unwrap("delta")`) {
		t.Fatalf("main.go = %q", got)
	}
}
