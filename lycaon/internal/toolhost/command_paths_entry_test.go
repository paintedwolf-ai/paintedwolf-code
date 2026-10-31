//go:build unix

package toolhost

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// TestArgvEchoHelper is the external program the path tests launch: it
// reports its working directory, PWD, TMPDIR and each argument, or reads or
// writes one file.
func TestArgvEchoHelper(t *testing.T) {
	if os.Getenv("PW_ARGV_ECHO") != "1" {
		return
	}
	args := flag.Args()
	switch {
	case len(args) == 2 && args[0] == "read":
		data, err := os.ReadFile(args[1])
		if err != nil {
			fmt.Println("error=" + err.Error())
			os.Exit(1)
		}
		fmt.Print("read=" + string(data))
	case len(args) == 3 && args[0] == "write":
		if err := os.WriteFile(args[1], []byte(args[2]+"\n"), 0o600); err != nil {
			fmt.Println("error=" + err.Error())
			os.Exit(1)
		}
	default:
		cwd, _ := os.Getwd()
		fmt.Println("cwd=" + cwd)
		fmt.Println("pwd=" + os.Getenv("PWD"))
		fmt.Println("tmpdir=" + os.Getenv("TMPDIR"))
		for _, arg := range args {
			fmt.Println("arg=" + arg)
		}
	}
	os.Exit(0)
}

// pathsFixture is a program entry over two attached folders, session scratch,
// the root history the session context is captured from, and path-bound owners.
type pathsFixture struct {
	*programtest.Fixture
	api, web projectroot.RootRef
	scratch  string
	history  *projectroot.History
	roots    []projectroot.RootRef
	active   string
	helper   string
	catRuns  int
	// onHead runs when the fixture head utility is reached mid-program.
	onHead func()
}

func newPathsFixture(t *testing.T) *pathsFixture {
	t.Helper()
	helper, err := os.Executable()
	testutil.FailErr(t, "locate test binary", err)
	f := &pathsFixture{history: &projectroot.History{}, helper: helper}
	real := func() string {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		testutil.FailErr(t, "resolve fixture directory", err)
		return dir
	}
	f.scratch = real()
	f.web = projectroot.RootRef{ID: "web-id", Label: "web", Path: real()}
	f.Fixture = programtest.New(t, programtest.Options{
		Owners: f.owners(),
		Context: func(dir string, tc *tools.ToolContext) {
			tc.Source.Roots, tc.Source.ActiveRootID = append([]projectroot.RootRef(nil), f.roots...), f.active
			tc.Source.RootHistory = f.history.View(tc.Identity.ProjectID)
			tc.Host.SessionScratchDir = f.scratch
		},
	})
	dir, err := filepath.EvalSymlinks(f.Dir)
	testutil.FailErr(t, "resolve primary fixture", err)
	f.api = projectroot.RootRef{ID: "api-id", Label: "api", Path: dir, IsPrimary: true}
	f.roots, f.active = []projectroot.RootRef{f.api, f.web}, f.api.ID
	for path, content := range map[string]string{
		filepath.Join(f.api.Path, "go.mod"):        "module api\n",
		filepath.Join(f.web.Path, "index.html"):    "<p>web</p>\n",
		filepath.Join(f.web.Path, "src", "app.js"): "app\n",
	} {
		testutil.FailErr(t, "mkdir fixture", os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "write fixture", os.WriteFile(path, []byte(content), 0o600))
	}
	return f
}

// owners uses the production owner set. The observation hooks place a root
// change during an actual reached owner; its result and refusal stay untouched.
func (f *pathsFixture) owners() func(programtest.OwnerPorts) func(tools.ToolContext, *projectpaths.ProgramPaths, *toolcontract.ProgramMemory, tooloutput.SpillAdmission) (command.ProgramOwners, error) {
	return func(ports programtest.OwnerPorts) func(tools.ToolContext, *projectpaths.ProgramPaths, *toolcontract.ProgramMemory, tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		factory, initErr := (programOwnerTools{boundary: ports.Boundary, operations: mutation.Operations{
			Write: &native.WriteTool{Boundary: ports.Boundary}, Delete: &native.DeleteTool{Boundary: ports.Boundary}, Move: &native.MoveTool{Boundary: ports.Boundary}, Copy: &native.CopyTool{Boundary: ports.Boundary}, Mkdir: &native.MkdirTool{Boundary: ports.Boundary}, Chmod: &native.ChmodTool{Boundary: ports.Boundary}, Chown: &native.ChownTool{Boundary: ports.Boundary}, Times: &native.TimesTool{Boundary: ports.Boundary}, Extract: &native.ExtractArchiveTool{Boundary: ports.Boundary},
		}}).compose(ports.Recorder)
		return func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, admission tooloutput.SpillAdmission) (command.ProgramOwners, error) {
			if initErr != nil {
				return command.ProgramOwners{}, initErr
			}
			owners, err := factory(tc, paths, memory, admission)
			if err != nil {
				return command.ProgramOwners{}, err
			}
			for name, observe := range map[string]func(){"cat": func() { f.catRuns++ }, "head": func() {
				if f.onHead != nil {
					f.onHead()
				}
			}} {
				owner := owners.Natives[name]
				original := owner.Prepare
				owner.Prepare = func(ctx context.Context, frame *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
					prepared, err := original(ctx, frame, dispatch, request)
					if err != nil {
						return command.NativePrepared{}, err
					}
					invoke := prepared.Invoke
					prepared.Invoke = func(ctx context.Context, tc tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
						observe()
						return invoke(ctx, tc, streams)
					}
					return prepared, nil
				}
				owners.Natives[name] = owner
			}
			return owners, nil
		}
	}
}

// echo launches the helper as an external program with words as its argv.
func (f *pathsFixture) echo(words string) string {
	return "PW_ARGV_ECHO=1 external -- " + strconv.Quote(f.helper) + " '-test.run=^TestArgvEchoHelper$' -- " + words
}

func (f *pathsFixture) stdout(t *testing.T, source string) string {
	t.Helper()
	out, err := f.Call(t, t.Context(), source)
	testutil.FailErr(t, "run program", err)
	outcome := decodeProgramOutcome(t, out)
	if outcome.Status != 0 || outcome.Refusal != nil {
		t.Fatalf("program status %d stderr %q", outcome.Status, outcome.Stderr)
	}
	return outcome.Stdout
}

func lines(values ...string) string { return strings.Join(values, "\n") + "\n" }

// refused runs a program the host stops and returns its outcome.
func (f *pathsFixture) refused(t *testing.T, source, code string) command.ProgramOutcome {
	t.Helper()
	out, err := f.Call(t, t.Context(), source)
	testutil.FailErr(t, "run refused program", err)
	outcome := decodeProgramOutcome(t, out)
	if outcome.Refusal == nil || outcome.Refusal.Code != code || !strings.Contains(outcome.Refusal.Feedback, code) {
		t.Fatalf("%s: refusal = %+v stderr %q, want %s", source, outcome.Refusal, outcome.Stderr, code)
	}
	return outcome
}

func TestProgramMovesBetweenAttachedFoldersWithAbsolutePaths(t *testing.T) {
	f := newPathsFixture(t)
	src := filepath.Join(f.web.Path, "src")
	got := f.stdout(t, `pwd
cd @web/src && pwd
`+f.echo(`@api/go.mod "@api/go.mod" '@web' rel @types/node`)+`
cd - >/dev/null && pwd
cd `+f.web.Path+`; cat src/app.js; (cd src; pwd); cd src | pwd; pwd
cat @api/go.mod`)
	want := lines(f.api.Path, src,
		"cwd="+src, "pwd="+src, "tmpdir="+f.scratch,
		"arg="+filepath.Join(f.api.Path, "go.mod"), "arg=@api/go.mod", "arg=@web", "arg=rel", "arg=@types/node",
		f.api.Path, "app", src, f.web.Path, f.web.Path, "module api")
	if got != want {
		t.Fatalf("stdout =\n%s\nwant\n%s", got, want)
	}
	// A program starts without OLDPWD.
	out, err := f.Call(t, t.Context(), "cd -")
	testutil.FailErr(t, "run cd - without OLDPWD", err)
	if outcome := decodeProgramOutcome(t, out); outcome.Status != 1 || outcome.Refusal != nil || outcome.Stderr != "cd: OLDPWD not set\n" {
		t.Fatalf("cd - without OLDPWD: status %d stderr %q refusal %+v", outcome.Status, outcome.Stderr, outcome.Refusal)
	}
	// Each program starts again at the session folder.
	if got := f.stdout(t, `pwd; echo_dir=$PWD; `+f.echo(`"$echo_dir"`)); got != lines(f.api.Path, "cwd="+f.api.Path, "pwd="+f.api.Path, "tmpdir="+f.scratch, "arg="+f.api.Path) {
		t.Fatalf("next program = %q", got)
	}
}

func TestScratchRoundTripsBetweenNativeAndExternal(t *testing.T) {
	f := newPathsFixture(t)
	got := f.stdout(t, `p=$(mktemp)
basename native-bytes > "$p"
`+f.echo(`read "$p"`)+`
q="$TMPDIR/from-external"
`+f.echo(`write "$q" external-bytes`)+`
cat "$q"
cat @scratch/from-external
cd "$TMPDIR" && pwd
basename "$p" > copy-name
cat "$SCRATCH_DIR/copy-name" | `+f.echo(`read /dev/stdin`))
	entries, err := os.ReadDir(f.scratch)
	testutil.FailErr(t, "list scratch", err)
	var made string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "tmp.") {
			made = entry.Name()
		}
	}
	want := lines("read=native-bytes", "external-bytes", "external-bytes", f.scratch, "read="+made)
	if made == "" || got != want {
		t.Fatalf("stdout =\n%s\nwant\n%s", got, want)
	}
	// A literal /tmp path keeps its OS meaning: it is not scratch.
	if filepath.Clean(f.scratch) == "/tmp" {
		t.Fatalf("fixture scratch %q aliases literal /tmp", f.scratch)
	}
	out, err := f.Call(t, t.Context(), "cat /tmp/from-external")
	testutil.FailErr(t, "read literal /tmp/from-external", err)
	if outcome := decodeProgramOutcome(t, out); outcome.Status != 1 || outcome.Stdout != "" {
		t.Fatalf("literal /tmp/from-external resolved to scratch: %+v", outcome)
	}
}

func TestUnknownAndStaleLabelsAtEntry(t *testing.T) {
	f := newPathsFixture(t)
	testutil.FailErr(t, "literal directory", os.Mkdir(filepath.Join(f.api.Path, "@missing"), 0700))
	testutil.FailErr(t, "literal file", os.WriteFile(filepath.Join(f.api.Path, "@missing", "file"), []byte("literal\n"), 0600))
	if out := f.stdout(t, "cat @missing/file"); out != "literal\n" {
		t.Fatalf("unattached literal=%q", out)
	}
	// The web folder is removed between turns.
	f.history.Record(testdbseed.DefaultProjectID, f.roots, []projectroot.RootRef{f.api})
	f.roots = []projectroot.RootRef{f.api}
	for _, source := range []string{"cat " + filepath.Join(f.web.Path, "index.html"), "cd " + f.web.Path} {
		f.refused(t, source, tools.RootNotAttachedCode)
	}
	// A stale label refuses at its word, in shell voice too, for native and
	// external argv alike.
	for _, source := range []string{"cat @web/index.html", f.echo("@web/src")} {
		if outcome := f.refused(t, source, tools.RootNotAttachedCode); !strings.Contains(outcome.Stderr, "@web: not attached here [ROOT_NOT_ATTACHED]") {
			t.Fatalf("%s stderr = %q", source, outcome.Stderr)
		}
	}
}

func TestRootRemovedMidProgramRefusesLaterEffects(t *testing.T) {
	f := newPathsFixture(t)
	f.onHead = func() {
		f.history.Record(testdbseed.DefaultProjectID, []projectroot.RootRef{f.api, f.web}, []projectroot.RootRef{f.api})
	}
	// The read before the removal keeps its output beside the refusal.
	if outcome := f.refused(t, "cat @web/index.html; head; cat @web/src/app.js", tools.RootNotAttachedCode); outcome.Stdout != "<p>web</p>\n" {
		t.Fatalf("settled output = %q", outcome.Stdout)
	}
	// The read before the removal ran; the one after never reached its owner.
	if executions := f.Executions(t); f.catRuns != 1 || executions["read"] != "completed;completed;" {
		t.Fatalf("catRuns = %d executions = %v", f.catRuns, executions)
	}
	// An external launch from a program admitted before the removal refuses
	// at the launch door as well.
	f.history = &projectroot.History{}
	f.roots = []projectroot.RootRef{f.api, f.web}
	f.onHead = func() {
		f.history.Record(testdbseed.DefaultProjectID, []projectroot.RootRef{f.api, f.web}, []projectroot.RootRef{f.api})
	}
	f.refused(t, "head; "+f.echo("after"), tools.RootNotAttachedCode)
}

func TestPrimaryReplacementMovesWhereProgramsStart(t *testing.T) {
	f := newPathsFixture(t)
	web := f.web
	web.IsPrimary = true
	f.history.Record(testdbseed.DefaultProjectID, f.roots, []projectroot.RootRef{web})
	f.roots, f.active = []projectroot.RootRef{web}, web.ID
	if got := f.stdout(t, "pwd; cd; pwd"); got != lines(web.Path, web.Path) {
		t.Fatalf("after primary removal programs start at %q", got)
	}
	f.refused(t, "cat "+filepath.Join(f.api.Path, "go.mod"), tools.RootNotAttachedCode)
}
