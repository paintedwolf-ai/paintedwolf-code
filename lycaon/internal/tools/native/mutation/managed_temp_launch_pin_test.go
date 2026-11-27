package mutation_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func managedTempLaunchFixture(t *testing.T) *programtest.Fixture {
	t.Helper()
	return programtest.New(t, programtest.Options{Owners: func(ports programtest.OwnerPorts) func(tools.ToolContext, *projectpaths.ProgramPaths, *toolcontract.ProgramMemory, tooloutput.SpillAdmission) (command.ProgramOwners, error) {
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
			for _, name := range []string{"mktemp"} {
				tool, _ := owners.Operation(name)
				natives[name] = command.NativeOperation{Tool: tool, Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
					prepared, err := owners.Prepare(mutation.Program{Paths: frame}, request)
					if err != nil {
						return command.NativePrepared{}, err
					}
					return command.NativePrepared{Mode: prepared.Mode, Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
				}}
			}
			return command.ProgramOwners{
				DeclareLaunch: owners.DeclareLaunchInputs,
				Natives:       natives,
				Publication: func(admit tools.CommandOutputAdmission) commandprogram.OutputPublication {
					return &mutation.Publication{Owners: owners, Program: mutation.Program{Paths: paths}, Tools: tc, Admission: admit}
				},
				Redirections: func(frame *projectpaths.ProgramPaths, admit tools.CommandOutputAdmission) (commandprogram.InputRedirection, commandprogram.OutputPublication) {
					publication := &mutation.Publication{Owners: owners, Program: mutation.Program{Paths: frame}, Tools: tc, Admission: admit}
					return publication, publication
				},
			}, nil
		}
	}})
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestManagedTempDeclaredAtLaunchPin(t *testing.T) {
	t.Run("expected_provenance_on_managed_temp_handoff", func(t *testing.T) {
		f := managedTempLaunchFixture(t)
		raw, err := f.Call(t, t.Context(), `p=$(mktemp); printf 'same\n' > "$p"; external -- /bin/cat "$p"`)
		testutil.FailErr(t, "run managed-temp handoff", err)
		outcome := decodeMutationOutcome(t, raw)
		if outcome.Status != 0 || outcome.Refusal != nil || outcome.Stdout != "same\n" {
			t.Fatalf("handoff outcome = %+v", outcome)
		}

		page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, outcome.ProgramID, "", 50)
		testutil.FailErr(t, "read handoff receipts", err)
		var catExecID string
		for _, exec := range page.Executions {
			if strings.Contains(exec.Command, "/bin/cat") {
				catExecID = exec.ID
				if exec.EffectCertainty != string(toolcontract.EffectAttempted) {
					t.Fatalf("external launch effect_certainty = %q, want %q", exec.EffectCertainty, toolcontract.EffectAttempted)
				}
			}
		}
		if catExecID == "" {
			t.Fatalf("missing external /bin/cat execution in %+v", page.Executions)
		}

		rows, err := f.Database.QueryContext(t.Context(),
			`SELECT role, mode, basis, root_id, path, disk_sha256, representation, content_sha256, content_bytes FROM invocation_program_revision_pins WHERE execution_id = ?`,
			catExecID)
		testutil.FailErr(t, "query launch revision pins", err)
		defer func() { _ = rows.Close() }()
		count := 0
		wantDigest := sha256Hex("same\n")
		for rows.Next() {
			count++
			var role, mode, basis, rootID, path, diskSHA, rep, contentSHA string
			var size int64
			testutil.FailErr(t, "scan launch pin", rows.Scan(&role, &mode, &basis, &rootID, &path, &diskSHA, &rep, &contentSHA, &size))
			if role != string(toolcontract.SourceRevision) || mode != string(toolcontract.PublishedDiskRevision) || basis != string(toolcontract.DeclaredAtLaunch) || rootID != "" || !strings.HasPrefix(path, f.ScratchDir) || diskSHA != wantDigest || rep != string(toolcontract.OriginalEncodedBytes) || contentSHA != wantDigest || size != 5 {
				t.Fatalf("unexpected launch pin: role=%q mode=%q basis=%q root=%q path=%q disk=%q rep=%q content=%q size=%d",
					role, mode, basis, rootID, path, diskSHA, rep, contentSHA, size)
			}
		}
		testutil.FailErr(t, "iterate launch pins", rows.Err())
		if count != 1 {
			t.Fatalf("declared_at_launch pins for /bin/cat = %d, want 1", count)
		}
	})

	t.Run("changed_or_type_swapped_managed_inputs_refuse_without_launch", func(t *testing.T) {
		cases := []struct {
			name   string
			mutate string
		}{
			{"replaced_with_identical_bytes", `rm -f "$1" && printf "same\n" > "$1"`},
			{"swapped_with_symlink_to_identical_bytes", `target="${1}.target"; printf "same\n" > "$target" && rm -f "$1" && ln -s "$target" "$1"`},
			{"swapped_with_dangling_symlink", `rm -f "$1" && ln -s "${1}.missing" "$1"`},
			{"replaced_with_directory", `rm -f "$1" && mkdir "$1"`},
			{"mutated_in_place", `printf "changed\n" > "$1"`},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				f := managedTempLaunchFixture(t)
				var launches atomic.Int64
				configure := f.Configure
				f.Configure = func(tctx *tools.ToolContext) {
					if configure != nil {
						configure(tctx)
					}
					tctx.Source.RecordViewProcess = func(int) error {
						launches.Add(1)
						return nil
					}
				}
				script := `p=$(mktemp); printf 'same\n' > "$p"; external -- /bin/sh -c '` + tc.mutate + `' _ "$p"; external -- /bin/cat "$p"`
				raw, err := f.Call(t, t.Context(), script)
				testutil.FailErr(t, "run "+tc.name, err)
				outcome := decodeMutationOutcome(t, raw)
				if outcome.Refusal == nil || outcome.Refusal.Code != "COMMAND_REVISION_CONFLICT" {
					t.Fatalf("%s: outcome = %+v, want COMMAND_REVISION_CONFLICT", tc.name, outcome)
				}
				if outcome.Stdout != "" {
					t.Fatalf("%s: /bin/cat produced stdout=%q", tc.name, outcome.Stdout)
				}
				if got := launches.Load(); got != 2 {
					t.Fatalf("%s: launch spy recorded %d launches, want 2 (external printf + mutator /bin/sh; /bin/cat must not launch)", tc.name, got)
				}
				page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, outcome.ProgramID, "", 50)
				testutil.FailErr(t, "read "+tc.name+" receipts", err)
				var catExecID string
				for _, exec := range page.Executions {
					if strings.Contains(exec.Command, "/bin/cat") {
						catExecID = exec.ID
						if exec.State != "refused" || exec.EffectCertainty != string(toolcontract.NoEffect) {
							t.Fatalf("%s: /bin/cat state=%q effect_certainty=%q, want refused/%s",
								tc.name, exec.State, exec.EffectCertainty, toolcontract.NoEffect)
						}
					}
				}
				if catExecID == "" {
					t.Fatalf("%s: missing refused /bin/cat execution in %+v", tc.name, page.Executions)
				}
				var pins int
				testutil.FailErr(t, "count pins on "+tc.name, f.Database.QueryRowContext(t.Context(),
					`SELECT count(*) FROM invocation_program_revision_pins WHERE execution_id = ?`, catExecID).Scan(&pins))
				if pins != 0 {
					t.Fatalf("%s: forged %d launch pins on changed managed-temp input", tc.name, pins)
				}
			})
		}
	})

	t.Run("generic_non_managed_argv_not_classified", func(t *testing.T) {
		f := managedTempLaunchFixture(t)
		testutil.FailErr(t, "write plain project file", os.WriteFile(filepath.Join(f.Dir, "plain.txt"), []byte("hello\n"), 0o600))
		raw, err := f.Call(t, t.Context(), `p=$(mktemp); printf 'tmp\n' > "$p"; external -- /bin/cat plain.txt`)
		testutil.FailErr(t, "run external with generic argv", err)
		outcome := decodeMutationOutcome(t, raw)
		if outcome.Status != 0 || outcome.Refusal != nil || outcome.Stdout != "hello\n" {
			t.Fatalf("generic argv outcome = %+v", outcome)
		}

		page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, outcome.ProgramID, "", 50)
		testutil.FailErr(t, "read generic argv receipts", err)
		var catExecID string
		for _, exec := range page.Executions {
			if strings.Contains(exec.Command, "/bin/cat") {
				catExecID = exec.ID
			}
		}
		if catExecID == "" {
			t.Fatalf("missing /bin/cat execution in %+v", page.Executions)
		}
		var pins int
		testutil.FailErr(t, "count pins on generic argv", f.Database.QueryRowContext(t.Context(),
			`SELECT count(*) FROM invocation_program_revision_pins WHERE execution_id = ?`, catExecID).Scan(&pins))
		if pins != 0 {
			t.Fatalf("generic non-managed argv produced %d launch pins, want 0", pins)
		}
	})
}
