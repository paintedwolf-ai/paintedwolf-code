//go:build linux

package programtest_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"golang.org/x/sys/unix"
)

func TestNativeCommandByteInputProcess(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "byte-input-probe" {
		return
	}
	var st unix.Stat_t
	testutil.FailErr(t, "inspect input byte stream", unix.Fstat(0, &st))
	if st.Mode&unix.S_IFMT != unix.S_IFIFO {
		t.Fatal("file input was not handed over as a byte stream")
	}
	_ = unix.Fchmod(0, 0o640)
	_ = unix.Fchown(0, -1, os.Getgid())
	_ = unix.Futimes(0, []unix.Timeval{{Sec: 123456789}, {Sec: 123456789}})
	_ = unix.Fsetxattr(0, "user.stream-fixture", []byte("changed"), 0)
	raw, err := io.ReadAll(os.Stdin)
	testutil.FailErr(t, "consume complete input through EOF", err)
	fmt.Printf("byte-input:%s\n", raw)
	os.Exit(0)
}

func TestNativeCommandFileInputsCarryBytesWithoutSourceMetadata(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_SANDBOX_NETWORK", "deny")
	caps, _ := confine.LinuxCapabilities()
	if !caps.Available() {
		t.Fatalf("required Linux confinement unavailable: %+v", caps)
	}
	program := programtest.New(t, programtest.Options{})

	program.Runtime.Owners = func(tc tools.ToolContext, frame *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, admission tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		owners := &mutation.Owners{Revisions: native.ExecutionRevisions{Boundary: program.Runtime.Boundary, Memory: memory}, Pins: program.Recorder, Admission: admission}
		input := &mutation.Publication{Owners: owners, Program: mutation.Program{Paths: frame}, Tools: tc}
		return command.ProgramOwners{Input: input, Redirections: func(paths *projectpaths.ProgramPaths, admit tools.CommandOutputAdmission) (commandprogram.InputRedirection, commandprogram.OutputPublication) {
			return &mutation.Publication{Owners: owners, Program: mutation.Program{Paths: paths}, Tools: tc, Admission: admit}, nil
		}}, nil
	}
	root, readRoot := program.Dir, t.TempDir()
	source := filepath.Join(readRoot, "input")
	testutil.FailErr(t, "seed owned input", os.WriteFile(source, []byte("ordinary fixture bytes through EOF"), 0o644))
	testutil.FailErr(t, "seed owned input attribute", unix.Setxattr(source, "user.stream-fixture", []byte("original"), 0))
	fd, err := unix.Open(source, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	testutil.FailErr(t, "open owned atime fixture", err)
	flags, err := unix.IoctlGetInt(fd, unix.FS_IOC_GETFLAGS)
	testutil.FailErr(t, "read owned inode flags", err)
	testutil.FailErr(t, "isolate ordinary read atime in fixture", unix.IoctlSetPointerInt(fd, unix.FS_IOC_SETFLAGS, flags|0x80)) // FS_NOATIME_FL.
	testutil.FailErr(t, "close owned atime fixture", unix.Close(fd))
	var before unix.Stat_t
	testutil.FailErr(t, "inspect source before", unix.Stat(source, &before))
	c, on := confine.DefaultConfinement(confine.Request{Roots: []string{root}, ReadRoots: []string{readRoot}})
	if !on || confine.BoundaryOf(c).Filesystem.Verdict(confine.AccessWrite, source).Allowed {
		t.Fatal("fixture input must be outside effective write authority")
	}
	self, err := os.Executable()
	testutil.FailErr(t, "locate byte input process", err)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{{ID: "implement", Tools: map[string]bool{"read": true, "verify": true}}})
	registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	t.Cleanup(func() {
		testutil.FailErr(t, "close owned fixture registry", registry.Lifecycle.Close(context.Background()))
	})
	tool := &native.VerifyTool{Runner: hostcmd.NewRunner(), Boundary: boundary, Background: registry}
	program.Configure = func(tc *tools.ToolContext) {
		tc.Files.ReadRoots = []string{readRoot}
		tc.Files.ApprovedFileAccess = []hitl.GrantedPathDelta{{Path: grantedpath.Normalize(source)}}
	}
	for _, kind := range []string{"stdin_from", "stage input"} {
		t.Run(kind, func(t *testing.T) {
			tctx := tools.ToolContext{Identity: tools.InvocationIdentity{Agent: "implement", SessionID: "stream-fixture-" + kind}, Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "primary", Path: root, IsPrimary: true}}}, Files: tools.InvocationFiles{ReadRoots: []string{readRoot}, ApprovedFileAccess: []hitl.GrantedPathDelta{{Path: grantedpath.Normalize(source)}}}}
			line := strconv.Quote(self) + " -test.run=TestNativeCommandByteInputProcess -- byte-input-probe"
			var raw string
			if kind == "stdin_from" {
				raw, err = tool.Run(t.Context(), map[string]any{"command": line, "stdin_from": source, "wait_ms": float64(10000)}, tctx)
				testutil.FailErr(t, "verify native byte input", err)
				var result struct {
					Outcome  string `json:"outcome"`
					ExitCode int    `json:"exit_code"`
					Tail     string `json:"tail"`
				}
				testutil.FailErr(t, "decode verification result", json.Unmarshal([]byte(raw), &result))
				if result.Outcome != "passed" || result.ExitCode != 0 || !strings.Contains(result.Tail, "byte-input:ordinary fixture bytes through EOF") {
					t.Fatalf("verification input did not finish with complete bytes: %s", raw)
				}
			} else {
				raw, err = program.Call(t, t.Context(), line+" < "+strconv.Quote(source))
				testutil.FailErr(t, "execute program byte input", err)
				var result command.ProgramOutcome
				testutil.FailErr(t, "decode program outcome", json.Unmarshal([]byte(raw), &result))
				if result.State != "completed" || result.Status != 0 || !strings.Contains(result.Stdout, "byte-input:ordinary fixture bytes through EOF") {
					t.Fatalf("program input did not finish with complete bytes: %s", raw)
				}
			}
			var after unix.Stat_t
			testutil.FailErr(t, "inspect source after", unix.Stat(source, &after))
			if before.Mode != after.Mode || before.Uid != after.Uid || before.Gid != after.Gid || before.Atim != after.Atim || before.Mtim != after.Mtim || before.Ctim != after.Ctim {
				t.Fatalf("native read input changed source metadata: before=%+v after=%+v", before, after)
			}
			data := make([]byte, 16)
			n, err := unix.Getxattr(source, "user.stream-fixture", data)
			testutil.FailErr(t, "read unchanged input attribute", err)
			if string(data[:n]) != "original" {
				t.Fatal("native input changed the source attribute")
			}
		})
	}
}
