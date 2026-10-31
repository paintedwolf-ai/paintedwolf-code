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

func TestResolveSessionOperandsGivesProgramsRealPaths(t *testing.T) {
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
		stages, _, err := commandsurface.ResolveSessionOperands(scratchStages(t, tc.line), scratchDir, "")
		testutil.FailErr(t, "resolve "+tc.line, err)
		if !reflect.DeepEqual(stages[0].Args, tc.want) {
			t.Errorf("%q resolved to %q, want %q", tc.line, stages[0].Args, tc.want)
		}
	}
}

func TestResolveSessionOperandsCoversEveryStage(t *testing.T) {
	scratchDir := filepath.FromSlash("/state/scratch/chat-1")
	stages, changed, err := commandsurface.ResolveSessionOperands(
		scratchStages(t, "sort @scratch/a.txt | uniq && wc -l @scratch/a.txt"), scratchDir, "")
	testutil.FailErr(t, "resolve", err)
	want := filepath.Join(scratchDir, "a.txt")
	if !changed || stages[0].Args[0] != want || stages[2].Args[1] != want {
		t.Fatalf("stages = %+v, want both operands resolved", stages)
	}
}

func TestResolveSessionOperandsRefusesWhatCannotResolve(t *testing.T) {
	_, _, err := commandsurface.ResolveSessionOperands(scratchStages(t, "cat @scratch/a.txt"), "", "")
	var operandErr *commandsurface.SessionOperandError
	if !errors.Is(err, commandsurface.ErrSessionUnavailable) || !errors.As(err, &operandErr) || operandErr.Operand != "@scratch/a.txt" {
		t.Fatalf("no scratch folder: err = %v", err)
	}
	_, _, err = commandsurface.ResolveSessionOperands(scratchStages(t, "cat @scratch/../other/a.txt"), "/state/scratch/chat-1", "")
	if !errors.Is(err, projectroot.ErrPathEscape) {
		t.Fatalf("escape: err = %v, want ErrPathEscape", err)
	}
	_, changed, err := commandsurface.ResolveSessionOperands(scratchStages(t, "echo @types/node"), "", "")
	if err != nil || changed {
		t.Fatalf("a non-scratch address needs no folder: changed=%v err=%v", changed, err)
	}
}

func TestScratchGlobExpandsInTheFolder(t *testing.T) {
	scratchDir := globTree(t, "a.log", "b.log", "c.txt")
	stages, _, err := commandsurface.ResolveSessionOperands(scratchStages(t, "cat @scratch/*.log"), scratchDir, "")
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
	stages, _, err := commandsurface.ResolveSessionOperands(
		scratchStages(t, "tool '@scratch/keep' @types/node *.go"), filepath.FromSlash("/state/scratch/chat-1"), "")
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

func TestOutputOperandsUseTheirOwnHostDirectory(t *testing.T) {
	dir := filepath.FromSlash("/state/session-output/chat")
	stages, changed, err := commandsurface.ResolveSessionOperands(scratchStages(t, "cat @output/program.stdout"), "", dir)
	testutil.FailErr(t, "resolve output operand", err)
	if !changed || len(stages) != 1 || len(stages[0].Args) != 1 || stages[0].Args[0] != filepath.Join(dir, "program.stdout") {
		t.Fatalf("stages=%+v", stages)
	}
	_, _, err = commandsurface.ResolveSessionOperands(scratchStages(t, "cat @output/program.stdout"), "/state/scratch/chat", "")
	var operand *commandsurface.SessionOperandError
	if !errors.Is(err, commandsurface.ErrSessionUnavailable) || !errors.As(err, &operand) || operand.Area != projectroot.VirtualOutputLabel {
		t.Fatalf("missing output capability=%v", err)
	}
	_, _, err = commandsurface.ResolveSessionOperands(scratchStages(t, "cat @output/../other/file"), "", dir)
	if !errors.Is(err, projectroot.ErrPathEscape) {
		t.Fatalf("escaped output capability=%v", err)
	}
}
