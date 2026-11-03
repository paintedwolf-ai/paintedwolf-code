package toolhost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestOutputNamespaceExpansionAndNativeBindingKeepQuotedNamesLiteral(t *testing.T) {
	f := newOutputDeliveryFixture(t)
	testutil.FailErr(t, "host output", scratch.PublishCommandOutput(f.OutputDir, "program", "stdout", strings.NewReader("retained\n")))
	for _, name := range []string{"@output", "@scratch", "@root"} {
		testutil.FailErr(t, "literal namespace directory", os.Mkdir(filepath.Join(f.Dir, name), 0700))
		testutil.FailErr(t, "literal namespace file", os.WriteFile(filepath.Join(f.Dir, name, "program.stdout"), []byte(name+" literal\n"), 0600))
	}
	testutil.FailErr(t, "scratch input", os.WriteFile(filepath.Join(f.ScratchDir, "program.stdout"), []byte("scratch\n"), 0600))
	testutil.FailErr(t, "root input", os.WriteFile(filepath.Join(f.Dir, "program.stdout"), []byte("root\n"), 0600))
	for _, tc := range []struct{ source, want string }{
		{`printf '%s\n' @output/program.stdout '@output/program.stdout' "@output/program.stdout"`, filepath.Join(f.OutputDir, "program.stdout") + "\n@output/program.stdout\n@output/program.stdout\n"},
		{`cat @output/program.stdout '@output/program.stdout' "@output/program.stdout"`, "retained\n@output literal\n@output literal\n"},
		{`cat @scratch/program.stdout '@scratch/program.stdout'`, "scratch\n@scratch literal\n"},
		{`cat @root/program.stdout '@root/program.stdout'`, "root\n@root literal\n"},
		{`p='@output/program.stdout'; native_path -- "$p"; cat "$p"`, filepath.Join(f.OutputDir, "program.stdout") + "\n@output literal\n"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			raw, err := f.Call(t, t.Context(), tc.source)
			testutil.FailErr(t, "run namespace program", err)
			out := decodeProgramOutcome(t, raw)
			if out.Status != 0 || out.Stdout != tc.want {
				t.Fatalf("status=%d output=%q want=%q stderr=%q", out.Status, out.Stdout, tc.want, out.Stderr)
			}
		})
	}
}

func TestOutputGlobExpandsOnlyTheUnquotedAddress(t *testing.T) {
	f := newOutputDeliveryFixture(t)
	testutil.FailErr(t, "host output", scratch.PublishCommandOutput(f.OutputDir, "program", "stdout", strings.NewReader("saved\n")))
	testutil.FailErr(t, "literal directory", os.Mkdir(filepath.Join(f.Dir, "@output"), 0700))
	testutil.FailErr(t, "literal matching file", os.WriteFile(filepath.Join(f.Dir, "@output", "literal.stdout"), []byte("literal\n"), 0600))
	raw, err := f.Call(t, t.Context(), `printf '%s\n' @output/*.stdout '@output/'*.stdout`)
	testutil.FailErr(t, "run actual glob expansion", err)
	out := decodeProgramOutcome(t, raw)
	want := filepath.Join(f.OutputDir, "program.stdout") + "\n@output/literal.stdout\n"
	if out.Status != 0 || out.Stdout != want {
		t.Fatalf("status=%d output=%q want=%q", out.Status, out.Stdout, want)
	}
}

func TestMissingOutputCapabilityRefusesBeforeChildrenAndPublication(t *testing.T) {
	f := newOutputDeliveryFixture(t)
	f.Context = func(_ string, tc *tools.ToolContext) { tc.Host.SessionOutputDir = "" }
	target := filepath.Join(f.Dir, "marker")
	testutil.FailErr(t, "original target", os.WriteFile(target, []byte("original"), 0600))
	raw, err := f.Call(t, t.Context(), `printf before; printf changed > marker`)
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != tools.SessionOutputUnavailableCode || reject.Data["path"] != "@output" || raw != "" {
		t.Fatalf("raw=%q rejection=%+v err=%v", raw, reject, err)
	}
	if programs, children := f.Programs(t), f.Executions(t); len(programs) != 0 || len(children) != 0 {
		t.Fatalf("unavailable capability started program=%v children=%v", programs, children)
	}
	body, err := os.ReadFile(target)
	testutil.FailErr(t, "unchanged destination", err)
	if string(body) != "original" {
		t.Fatalf("publication occurred=%q", body)
	}
	entries, err := os.ReadDir(f.OutputDir)
	testutil.FailErr(t, "unpublished output folder", err)
	if len(entries) != 0 {
		t.Fatalf("unexpected copies=%v", entries)
	}
}
