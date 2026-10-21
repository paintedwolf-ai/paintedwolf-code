//go:build unix

package command_test

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
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/literal"
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
// the root history the session context is captured from, and the real
// literal and mutation owners.
type pathsFixture struct {
	*programtest.Fixture
	api, web projectroot.RootRef
	scratch  string
	history  *projectroot.History
	roots    []projectroot.RootRef
	active   string
	helper   string
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
		Owners: f.owners(t),
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

// owners composes the shipped literal and mutation owners the way toolhost
// does, plus a head fixture that marks a point in the program.
func (f *pathsFixture) owners(t *testing.T) func(programtest.OwnerPorts) func(tools.ToolContext, *projectpaths.ProgramPaths) (command.ProgramOwners, error) {
	return func(ports programtest.OwnerPorts) func(tools.ToolContext, *projectpaths.ProgramPaths) (command.ProgramOwners, error) {
		revisions := native.ExecutionRevisions{Boundary: ports.Boundary}
		literals, err := literal.NewOwners(revisions, ports.Recorder, literal.DomainOwners{})
		testutil.FailErr(t, "compose literal owners", err)
		b := ports.Boundary
		mutations, err := mutation.NewOwners(revisions, ports.Recorder, mutation.Operations{Write: &native.WriteTool{Boundary: b}, Delete: &native.DeleteTool{Boundary: b},
			Move: &native.MoveTool{Boundary: b}, Copy: &native.CopyTool{Boundary: b}, Mkdir: &native.MkdirTool{Boundary: b}, Chmod: &native.ChmodTool{Boundary: b},
			Chown: &native.ChownTool{Boundary: b}, Times: &native.TimesTool{Boundary: b}, Extract: &native.ExtractArchiveTool{Boundary: b}})
		testutil.FailErr(t, "compose mutation owners", err)
		return func(tc tools.ToolContext, paths *projectpaths.ProgramPaths) (command.ProgramOwners, error) {
			barrier := &native.PublicationBarrier{}
			publication := &mutation.Publication{Owners: mutations, Program: mutation.Program{Paths: paths, Barrier: barrier}, Tools: tc}
			owners := command.ProgramOwners{Natives: map[string]command.NativeOperation{}, Publication: publication, Input: publication, AdmitLaunch: barrier.AdmitDependentLaunch,
				Redirections: func(frame *projectpaths.ProgramPaths) (commandprogram.InputRedirection, commandprogram.OutputPublication) {
					frameOutput := &mutation.Publication{Owners: mutations, Program: mutation.Program{Paths: frame, Barrier: barrier}, Tools: tc}
					return frameOutput, frameOutput
				}}
			owners.Natives["cat"] = command.NativeOperation{Tool: "read", Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				prepared, err := literals.Prepare(frame, request)
				return command.NativePrepared{Review: prepared.Review, Invoke: prepared.InvokeOwner}, err
			}}
			owners.Natives["mktemp"] = command.NativeOperation{Tool: "write", Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				prepared, err := mutations.Prepare(mutation.Program{Paths: frame, Barrier: barrier}, request)
				return command.NativePrepared{Review: prepared.Review, Invoke: prepared.InvokeOwner}, err
			}}
			owners.Natives["head"] = command.NativeOperation{Tool: "read", Prepare: func(_ context.Context, _ *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				bound, err := commandprogram.CatalogBinder{}.Bind(catalogSpec("head"), request.Argv)
				if err != nil {
					return command.NativePrepared{}, err
				}
				return command.NativePrepared{Review: bound.Review("read"), Invoke: func(context.Context, tools.ToolContext, commandprogram.Streams) (toolcontract.ProducerResult, error) {
					if f.onHead != nil {
						f.onHead()
					}
					return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
				}}, nil
			}}
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
	outcome := decodeOutcome(t, out)
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
	outcome := decodeOutcome(t, out)
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
cd "$OLDPWD"; pwd
cd `+f.web.Path+`; cat src/app.js; (cd src; pwd); pwd
cat @api/go.mod`)
	want := lines(f.api.Path, src,
		"cwd="+src, "pwd="+src, "tmpdir="+f.scratch,
		"arg="+filepath.Join(f.api.Path, "go.mod"), "arg=@api/go.mod", "arg=@web", "arg=rel", "arg=@types/node",
		f.api.Path, "app", src, f.web.Path, "module api")
	if got != want {
		t.Fatalf("stdout =\n%s\nwant\n%s", got, want)
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
	if strings.HasPrefix(f.scratch, "/tmp/") {
		t.Fatalf("fixture scratch %q aliases literal /tmp", f.scratch)
	}
}

func TestUnknownAndStaleLabelsAtEntry(t *testing.T) {
	f := newPathsFixture(t)
	f.refused(t, "cat @missing/file", "UNKNOWN_ROOT_LABEL")
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
	if executions := f.Executions(t); strings.Count(executions["cat"], ";") != 1 || strings.Count(executions["head"], ";") != 1 {
		t.Fatalf("executions = %v", executions)
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
