package commandsurface_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
)

func scratchStages(t *testing.T, line string) []exec.Stage {
	t.Helper()
	plan, err := commandsurface.ParsePlan(map[string]any{"command": line})
	testutil.FailErr(t, "parse "+line, err)
	return plan.Stages
}

func TestResolveScratchOperandsGivesProgramsRealPaths(t *testing.T) {
	scratchDir := filepath.FromSlash("/state/scratch/chat-1")
	cases := []struct {
		line string
		want []string
	}{
		{line: "python3 @scratch/t.py --out @scratch/out.json", want: []string{
			filepath.Join(scratchDir, "t.py"), "--out", filepath.Join(scratchDir, "out.json"),
		}},
		{line: "ls @Scratch", want: []string{scratchDir}},
		{line: "cat '@scratch/literal.txt'", want: []string{"@scratch/literal.txt"}},
		{line: `cat "@scratch/literal.txt"`, want: []string{"@scratch/literal.txt"}},
		{line: "npm install @types/node", want: []string{"install", "@types/node"}},
		{line: "grep -e x@scratch/y z", want: []string{"-e", "x@scratch/y", "z"}},
	}
	for _, tc := range cases {
		stages, _, err := commandsurface.ResolveScratchOperands(scratchStages(t, tc.line), scratchDir)
		testutil.FailErr(t, "resolve "+tc.line, err)
		if !reflect.DeepEqual(stages[0].Args, tc.want) {
			t.Errorf("%q resolved to %q, want %q", tc.line, stages[0].Args, tc.want)
		}
	}
}

func TestResolveScratchOperandsCoversEveryStage(t *testing.T) {
	scratchDir := filepath.FromSlash("/state/scratch/chat-1")
	stages, changed, err := commandsurface.ResolveScratchOperands(
		scratchStages(t, "sort @scratch/a.txt | uniq && wc -l @scratch/a.txt"), scratchDir)
	testutil.FailErr(t, "resolve", err)
	want := filepath.Join(scratchDir, "a.txt")
	if !changed || stages[0].Args[0] != want || stages[2].Args[1] != want {
		t.Fatalf("stages = %+v, want both operands resolved", stages)
	}
}

func TestResolveScratchOperandsRefusesWhatCannotResolve(t *testing.T) {
	_, _, err := commandsurface.ResolveScratchOperands(scratchStages(t, "cat @scratch/a.txt"), "")
	var operandErr *commandsurface.ScratchOperandError
	if !errors.Is(err, commandsurface.ErrScratchUnavailable) || !errors.As(err, &operandErr) || operandErr.Operand != "@scratch/a.txt" {
		t.Fatalf("no scratch folder: err = %v", err)
	}
	_, _, err = commandsurface.ResolveScratchOperands(scratchStages(t, "cat @scratch/../other/a.txt"), "/state/scratch/chat-1")
	if !errors.Is(err, projectroot.ErrPathEscape) {
		t.Fatalf("escape: err = %v, want ErrPathEscape", err)
	}
	_, changed, err := commandsurface.ResolveScratchOperands(scratchStages(t, "echo @types/node"), "")
	if err != nil || changed {
		t.Fatalf("a non-scratch address needs no folder: changed=%v err=%v", changed, err)
	}
}

func TestScratchGlobExpandsInTheFolder(t *testing.T) {
	scratchDir := globTree(t, "a.log", "b.log", "c.txt")
	stages, _, err := commandsurface.ResolveScratchOperands(scratchStages(t, "cat @scratch/*.log"), scratchDir)
	testutil.FailErr(t, "resolve", err)
	expanded, changed, err := commandsurface.ExpandGlobs(context.Background(), stages, commandsurface.GlobScope{Dir: t.TempDir()})
	testutil.FailErr(t, "expand", err)
	want := []string{filepath.Join(scratchDir, "a.log"), filepath.Join(scratchDir, "b.log")}
	if !changed || !reflect.DeepEqual(expanded[0].Args, want) {
		t.Fatalf("expanded %q, want %q", expanded[0].Args, want)
	}
}

func TestRenderedStagesKeepLiteralAddressesLiteral(t *testing.T) {
	root := globTree(t, "x.go")
	stages, _, err := commandsurface.ResolveScratchOperands(
		scratchStages(t, "tool '@scratch/keep' @types/node *.go"), filepath.FromSlash("/state/scratch/chat-1"))
	testutil.FailErr(t, "resolve", err)
	stages, _, err = commandsurface.ExpandGlobs(context.Background(), stages, commandsurface.GlobScope{Dir: root})
	testutil.FailErr(t, "expand", err)
	rendered := commandsurface.RenderStages(stages)
	again := scratchStages(t, rendered)
	if !reflect.DeepEqual(again[0].Args, []string{"@scratch/keep", "@types/node", "x.go"}) {
		t.Fatalf("rendered %q reparsed to %q", rendered, again[0].Args)
	}
	if !reflect.DeepEqual(again[0].Addressed, []bool{false, true, false}) {
		t.Fatalf("rendered %q reparsed with address marks %v, want [false true false]", rendered, again[0].Addressed)
	}
}
