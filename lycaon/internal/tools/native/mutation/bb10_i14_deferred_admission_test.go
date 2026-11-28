package mutation_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type deferredWindowBarrier struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int64
}

func (b *deferredWindowBarrier) OpenCommandWindow(ctx context.Context, _ sourceledger.CommandWindowInput) (*sourceledger.OpenCommandWindow, error) {
	if b.calls.Add(1) == 1 {
		close(b.entered)
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, errors.New("deferred test window barrier")
}

type printfBuiltin struct{}

func (printfBuiltin) Invoke(_ context.Context, req commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	if len(req.Argv) >= 2 && streams.Output != nil {
		text := strings.ReplaceAll(req.Argv[1].Value, `\n`, "\n")
		_, _ = fmt.Fprint(streams.Output, text)
	}
	return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
}

func i14DeferredFixture(t *testing.T, barrier *deferredWindowBarrier, launches *atomic.Int64) *programtest.Fixture {
	t.Helper()
	f := programtest.New(t, programtest.Options{Owners: func(ports programtest.OwnerPorts) func(tools.ToolContext, *projectpaths.ProgramPaths, *toolcontract.ProgramMemory, tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		boundary := ports.Boundary
		mutations, err := mutation.NewOwners(native.ExecutionRevisions{Boundary: boundary}, ports.Recorder, mutation.Operations{
			Write: &native.WriteTool{Boundary: boundary}, Delete: &native.DeleteTool{Boundary: boundary},
			Move: &native.MoveTool{Boundary: boundary}, Copy: &native.CopyTool{Boundary: boundary},
			Mkdir: &native.MkdirTool{Boundary: boundary}, Chmod: &native.ChmodTool{Boundary: boundary},
			Chown: &native.ChownTool{Boundary: boundary}, Times: &native.TimesTool{Boundary: boundary},
			Extract: &native.ExtractArchiveTool{Boundary: boundary},
		})
		testutil.FailErr(t, "mutation operations", err)
		return func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, admission tooloutput.SpillAdmission) (command.ProgramOwners, error) {
			program := *mutations
			program.Revisions.Memory, program.Admission = memory, admission
			program.ManagedTemps = mutation.NewManagedTempTracker()
			owners := &program
			natives := map[string]command.NativeOperation{}
			tool, _ := owners.Operation("mktemp")
			natives["mktemp"] = command.NativeOperation{Tool: tool, Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				prepared, err := owners.Prepare(mutation.Program{Paths: frame}, request)
				if err != nil {
					return command.NativePrepared{}, err
				}
				return command.NativePrepared{Mode: prepared.Mode, Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
			}}
			return command.ProgramOwners{
				DeclareLaunch: owners.DeclareLaunchInputs,
				Natives:       natives,
				Builtins: func(command.BuiltinPorts) (map[string]commandprogram.OperationDispatcher, func(), error) {
					return map[string]commandprogram.OperationDispatcher{"printf": printfBuiltin{}}, func() {}, nil
				},
				Publication: func(admit tools.CommandOutputAdmission) commandprogram.OutputPublication {
					return &mutation.Publication{Owners: owners, Program: mutation.Program{Paths: paths}, Tools: tc, Admission: admit}
				},
				Redirections: func(frame *projectpaths.ProgramPaths, admit tools.CommandOutputAdmission) (commandprogram.InputRedirection, commandprogram.OutputPublication) {
					pub := &mutation.Publication{Owners: owners, Program: mutation.Program{Paths: frame}, Tools: tc, Admission: admit}
					return pub, pub
				},
			}, nil
		}
	}})
	configure := f.Configure
	f.Configure = func(tc *tools.ToolContext) {
		if configure != nil {
			configure(tc)
		}
		tc.Source.Commands = barrier
		tc.Source.RecordViewProcess = func(int) error {
			launches.Add(1)
			return nil
		}
	}
	return f
}

func findSingleTempFile(t *testing.T, scratchDir string) string {
	t.Helper()
	entries, err := os.ReadDir(scratchDir)
	testutil.FailErr(t, "readdir scratch", err)
	var found string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "tmp.") || strings.HasPrefix(entry.Name(), "lycaon-") || !entry.IsDir() {
			if found == "" {
				found = filepath.Join(scratchDir, entry.Name())
			}
		}
	}
	if found == "" {
		t.Fatalf("no managed temp file found in %s", scratchDir)
	}
	return found
}

// TestBB10I14DeferredAdmissionNamesSameResource verifies BB10 I14 by suspending
// external launch at the real observation-window waiting boundary immediately
// before launch-input verification and varying the target in place.
func TestBB10I14DeferredAdmissionNamesSameResource(t *testing.T) {
	cases := []struct {
		name           string
		script         string
		mutateAtWait   func(t *testing.T, tempPath string)
		wantRefusal    string
		wantStdout     string
		wantLaunches   int64
		wantPinCount   int
		wantDigestText string
	}{
		{
			name:           "unchanged_managed_temp",
			script:         `p=$(mktemp); printf 'same\n' > "$p"; external -- /bin/cat "$p"`,
			mutateAtWait:   func(*testing.T, string) {},
			wantStdout:     "same\n",
			wantLaunches:   1,
			wantPinCount:   1,
			wantDigestText: "same\n",
		},
		{
			name:   "newer_published_bytes_during_wait",
			script: `p=$(mktemp); printf 'same\n' > "$p"; external -- /bin/cat "$p"`,
			mutateAtWait: func(t *testing.T, tempPath string) {
				testutil.FailErr(t, "mutate bytes", os.WriteFile(tempPath, []byte("newer\n"), 0o600))
			},
			wantRefusal:  "COMMAND_REVISION_CONFLICT",
			wantLaunches: 0,
			wantPinCount: 0,
		},
		{
			name:   "equal_byte_replacement_new_inode_during_wait",
			script: `p=$(mktemp); printf 'same\n' > "$p"; external -- /bin/cat "$p"`,
			mutateAtWait: func(t *testing.T, tempPath string) {
				before, err := os.Lstat(tempPath)
				testutil.FailErr(t, "lstat before", err)
				replacement := tempPath + ".new"
				testutil.FailErr(t, "write replacement", os.WriteFile(replacement, []byte("same\n"), 0o600))
				testutil.FailErr(t, "rename replacement", os.Rename(replacement, tempPath))
				after, err := os.Lstat(tempPath)
				testutil.FailErr(t, "lstat after", err)
				if os.SameFile(before, after) {
					t.Fatalf("expected distinct physical inode after rename")
				}
			},
			wantRefusal:  "COMMAND_REVISION_CONFLICT",
			wantLaunches: 0,
			wantPinCount: 0,
		},
		{
			name:   "changed_path_resolution_symlink_during_wait",
			script: `p=$(mktemp); printf 'same\n' > "$p"; external -- /bin/cat "$p"`,
			mutateAtWait: func(t *testing.T, tempPath string) {
				target := tempPath + ".target"
				testutil.FailErr(t, "write target", os.WriteFile(target, []byte("same\n"), 0o600))
				testutil.FailErr(t, "remove original", os.Remove(tempPath))
				testutil.FailErr(t, "symlink target", os.Symlink(target, tempPath))
			},
			wantRefusal:  "COMMAND_REVISION_CONFLICT",
			wantLaunches: 0,
			wantPinCount: 0,
		},
		{
			name:   "changed_type_directory_during_wait",
			script: `p=$(mktemp); printf 'same\n' > "$p"; external -- /bin/cat "$p"`,
			mutateAtWait: func(t *testing.T, tempPath string) {
				testutil.FailErr(t, "remove original", os.Remove(tempPath))
				testutil.FailErr(t, "mkdir replacement", os.Mkdir(tempPath, 0o700))
			},
			wantRefusal:  "COMMAND_REVISION_CONFLICT",
			wantLaunches: 0,
			wantPinCount: 0,
		},
		{
			name:         "unrelated_literal_operand",
			script:       `p=$(mktemp); printf 'tmp\n' > "$p"; external -- /bin/cat plain.txt`,
			mutateAtWait: func(*testing.T, string) {},
			wantStdout:   "hello\n",
			wantLaunches: 1,
			wantPinCount: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			barrier := &deferredWindowBarrier{entered: make(chan struct{}), release: make(chan struct{})}
			var launches atomic.Int64
			f := i14DeferredFixture(t, barrier, &launches)
			testutil.FailErr(t, "seed plain.txt", os.WriteFile(filepath.Join(f.Dir, "plain.txt"), []byte("hello\n"), 0o600))

			type callResult struct {
				raw string
				err error
			}
			done := make(chan callResult, 1)
			go func() {
				raw, err := f.Call(t, t.Context(), tc.script)
				done <- callResult{raw: raw, err: err}
			}()

			<-barrier.entered
			tempPath := findSingleTempFile(t, f.ScratchDir)
			tc.mutateAtWait(t, tempPath)
			close(barrier.release)

			res := <-done
			testutil.FailErr(t, "run "+tc.name, res.err)
			outcome := decodeMutationOutcome(t, res.raw)
			if tc.wantRefusal != "" {
				if outcome.Refusal == nil || outcome.Refusal.Code != tc.wantRefusal || outcome.Stdout != "" {
					t.Fatalf("%s: outcome=%+v refusal=%+v, want %s", tc.name, outcome, outcome.Refusal, tc.wantRefusal)
				}
			} else if outcome.Refusal != nil || outcome.Status != 0 || outcome.Stdout != tc.wantStdout {
				t.Fatalf("%s: outcome=%+v refusal=%+v, want stdout=%q", tc.name, outcome, outcome.Refusal, tc.wantStdout)
			}
			if got := launches.Load(); got != tc.wantLaunches {
				t.Fatalf("%s: launches=%d, want %d", tc.name, got, tc.wantLaunches)
			}

			page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, outcome.ProgramID, "", 50)
			testutil.FailErr(t, "read receipts", err)
			var catExecID string
			for _, exec := range page.Executions {
				if strings.Contains(exec.Command, "/bin/cat") {
					catExecID = exec.ID
					if tc.wantRefusal != "" && (exec.State != "refused" || exec.EffectCertainty != string(toolcontract.NoEffect)) {
						t.Fatalf("%s: /bin/cat state=%q certainty=%q", tc.name, exec.State, exec.EffectCertainty)
					}
				}
			}
			if catExecID == "" {
				t.Fatalf("%s: missing /bin/cat execution in %+v", tc.name, page.Executions)
			}
			rows, err := f.Database.QueryContext(t.Context(),
				`SELECT basis, disk_sha256, content_bytes FROM invocation_program_revision_pins WHERE execution_id = ?`, catExecID)
			testutil.FailErr(t, "query pins", err)
			defer func() { _ = rows.Close() }()
			pins := 0
			for rows.Next() {
				pins++
				var basis, digest string
				var size int64
				testutil.FailErr(t, "scan pin", rows.Scan(&basis, &digest, &size))
				if basis != string(toolcontract.DeclaredAtLaunch) || digest != sha256Hex(tc.wantDigestText) || size != int64(len(tc.wantDigestText)) {
					t.Fatalf("%s: unexpected pin basis=%q digest=%q size=%d", tc.name, basis, digest, size)
				}
			}
			testutil.FailErr(t, "iterate pins", rows.Err())
			if pins != tc.wantPinCount {
				t.Fatalf("%s: pins=%d, want %d", tc.name, pins, tc.wantPinCount)
			}
		})
	}
}
