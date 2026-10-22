package command_test

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/hostenv"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type echoDispatcher struct{}

func (echoDispatcher) Invoke(_ context.Context, req commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	argv := req.Argv.Values()
	_, err := io.WriteString(streams.Output, strings.Join(argv[1:], " ")+"\n")
	if err != nil {
		return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
	}
	return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
}

func newIdiomsFixture(t *testing.T) *programFixture {
	t.Helper()
	f := newProgramFixture(t)
	hostUtilities, err := hostenv.NewOwners(hostenv.OSHost{}, time.Now, f.Runtime.Boundary)
	testutil.FailErr(t, "hostenv owners", err)
	f.Runtime.Builtins = func(tc tools.ToolContext, nested commandprogram.NestedCommand, bound command.NativeBindings) map[string]commandprogram.OperationDispatcher {
		builtins := hostUtilities.Builtins(tc, nested, bound)
		builtins["echo"] = echoDispatcher{}
		return builtins
	}
	f.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths) (command.ProgramOwners, error) {
		return command.ProgramOwners{
			Natives: map[string]command.NativeOperation{
				"tee":  teeOperation(f.Dir),
				"head": firstLineOperation(),
			},
		}, nil
	}
	return f
}

func TestProgramEntryWhichAndCommandVPrintExecutablePaths(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed on PATH")
	}
	f := newIdiomsFixture(t)
	testutil.FailErr(t, "git init", exec.Command("git", "-C", f.Dir, "init", "-q").Run())

	out, err := f.call(t, t.Context(), `
w=$(which git)
c=$(command -v git)
t=$(type git)
cv=$(command -V git)
echo "which=$w"
echo "command_v=$c"
echo "type=$t"
echo "command_V=$cv"
"$w" status --porcelain
`)
	testutil.FailErr(t, "run which/command/type", err)
	outcome := decodeOutcome(t, out)
	if outcome.Status != 0 || outcome.State != "completed" {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	wantLines := []string{
		"which=" + gitPath,
		"command_v=" + gitPath,
		"type=git is a native built-in",
		"command_V=git is a native built-in",
	}
	for _, want := range wantLines {
		if !strings.Contains(outcome.Stdout, want+"\n") {
			t.Fatalf("stdout %q missing %q", outcome.Stdout, want)
		}
	}

	// With ExternalHidden enabled, running the resolved path "$BIN" goes through
	// the normal external launch path while explicit `external --` is refused.
	f.Runtime.ExternalHidden = true
	out, err = f.call(t, t.Context(), `BIN=$(which git); "$BIN" status --porcelain; echo status=$?`)
	testutil.FailErr(t, "run $BIN with ExternalHidden", err)
	outcome = decodeOutcome(t, out)
	if outcome.Status != 0 || outcome.Refusal != nil || !strings.Contains(outcome.Stdout, "status=0\n") {
		t.Fatalf("resolved binary path failed under ExternalHidden: %+v", outcome)
	}
}

func TestProgramEntryIdiomsRedirectsProcessSubstAndTraps(t *testing.T) {
	f := newIdiomsFixture(t)
	testutil.FailErr(t, "seed a.txt", os.WriteFile(filepath.Join(f.Dir, "a.txt"), []byte("b\na\nc\n"), 0o600))
	testutil.FailErr(t, "seed b.txt", os.WriteFile(filepath.Join(f.Dir, "b.txt"), []byte("b\na\nd\n"), 0o600))

	out, err := f.call(t, t.Context(), `
trap 'echo trap_exit=$?' EXIT
arr=(first second)
arr+=(third)
((i = 0))
while ((i < ${#arr[@]})); do
	((i++))
done
s="hello_world"
echo "count=$i slice=${s:0:5} rep=${s/_/-} all=${arr[*]}"
echo err_msg >&2
comm -12 <(sort a.txt) <(sort b.txt)
python3 -c 'import sys; print("py:" + open(sys.argv[1]).read().strip())' <(echo from_proc_subst)
`)
	testutil.FailErr(t, "run idioms program", err)
	outcome := decodeOutcome(t, out)
	if outcome.Status != 0 || outcome.State != "completed" {
		t.Fatalf("unexpected outcome: %+v (raw: %s)", outcome, out)
	}
	for _, want := range []string{
		"count=3 slice=hello rep=hello-world all=first second third\n",
		"a\nb\n",
		"py:from_proc_subst\n",
		"trap_exit=0\n",
	} {
		if !strings.Contains(outcome.Stdout, want) {
			t.Fatalf("stdout %q missing %q", outcome.Stdout, want)
		}
	}
	if outcome.Stderr != "err_msg\n" {
		t.Fatalf("stderr = %q, want %q", outcome.Stderr, "err_msg\n")
	}
}
