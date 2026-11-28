package command_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func newProgressEnvelopeFixture(t *testing.T, allowProgress bool) *programtest.Fixture {
	t.Helper()
	f := programtest.New(t, programtest.Options{
		Natives: func(dir string) map[string]command.NativeOperation {
			return map[string]command.NativeOperation{"tee": teeOperation(dir)}
		},
	})
	testutil.FailErr(t, "clear initial progress", f.SetProgress(""))
	schemas, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "load platform tool schemas", err)
	f.Runtime.Executor.Metadata.SetToolSchemas(schemas)
	testutil.FailErr(t, "register update_progress metadata", f.Runtime.Registry.Register(
		"update_progress",
		func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			t.Fatal("recursive JSON handler invoked")
			return "", nil
		},
	))
	classes, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandExternalExec)
	testutil.FailErr(t, "compile command effects", err)
	profileTools := map[string]bool{"command": true, "write": true, "read": true}
	if allowProgress {
		profileTools["update_progress"] = true
	}
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{
		{ID: "fixture", Tools: profileTools, CommandEffects: classes},
	})
	policy := toolprofiles.NewProfilePolicyEngine(boundary)
	f.Runtime.Boundary = boundary
	f.Runtime.Executor.Metadata.SetPolicy(policy)
	return f
}

func TestProgramProgressEnvelopeInvalidHasZeroEffects(t *testing.T) {
	t.Run("invalid_syntax", func(t *testing.T) {
		f := newProgressEnvelopeFixture(t, true)
		_, err := f.CallArgs(t, t.Context(), map[string]any{
			"command": "printf 'never\\n' | tee never.txt; if",
			"progress_update": map[string]any{
				"content": "- [ ] Create never.txt",
			},
		})
		if err == nil {
			t.Fatal("invalid syntax accepted")
		}
		if got := f.Progress(t.Context()); got != "" {
			t.Fatalf("invalid syntax committed progress: %q", got)
		}
		if _, statErr := os.Stat(filepath.Join(f.Dir, "never.txt")); !os.IsNotExist(statErr) {
			t.Fatalf("invalid syntax created file: %v", statErr)
		}
		if programs := f.Programs(t); len(programs) != 0 {
			t.Fatalf("invalid syntax recorded program receipts: %v", programs)
		}
	})

	t.Run("invalid_envelope_schema", func(t *testing.T) {
		f := newProgressEnvelopeFixture(t, true)
		_, err := f.CallArgs(t, t.Context(), map[string]any{
			"command": "printf 'never\\n' | tee never.txt",
			"progress_update": map[string]any{
				"content":       "- [ ] Create never.txt",
				"unknown_field": true,
			},
		})
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != "TOOL_ARGS_INVALID" {
			t.Fatalf("invalid envelope err = %v, want TOOL_ARGS_INVALID", err)
		}
		if got := f.Progress(t.Context()); got != "" {
			t.Fatalf("invalid envelope committed progress: %q", got)
		}
		if _, statErr := os.Stat(filepath.Join(f.Dir, "never.txt")); !os.IsNotExist(statErr) {
			t.Fatalf("invalid envelope created file: %v", statErr)
		}
		if programs := f.Programs(t); len(programs) != 0 {
			t.Fatalf("invalid envelope recorded program receipts: %v", programs)
		}
	})

	t.Run("invalid_checklist_content", func(t *testing.T) {
		f := newProgressEnvelopeFixture(t, true)
		_, err := f.CallArgs(t, t.Context(), map[string]any{
			"command": "printf 'never\\n' | tee never.txt",
			"progress_update": map[string]any{
				"content": "## Progress\n- [ ] top\n  - [ ] nested\n",
			},
		})
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != "PROGRESS_NESTED_LINE" {
			t.Fatalf("invalid checklist err = %v, want PROGRESS_NESTED_LINE", err)
		}
		if got := f.Progress(t.Context()); got != "" {
			t.Fatalf("invalid checklist committed progress: %q", got)
		}
		if _, statErr := os.Stat(filepath.Join(f.Dir, "never.txt")); !os.IsNotExist(statErr) {
			t.Fatalf("invalid checklist created file: %v", statErr)
		}
		if programs := f.Programs(t); len(programs) != 0 {
			t.Fatalf("invalid checklist recorded program receipts: %v", programs)
		}
	})

	t.Run("owner_rejection_stops_before_command", func(t *testing.T) {
		f := newProgressEnvelopeFixture(t, true)
		_, err := f.CallArgs(t, t.Context(), map[string]any{
			"command": "printf 'never\\n' | tee never.txt",
			"progress_update": map[string]any{
				"content": "not a markdown checklist",
			},
		})
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != "PROGRESS_NO_OPEN_STEP" {
			t.Fatalf("owner rejection err = %v, want PROGRESS_NO_OPEN_STEP", err)
		}
		if got := f.Progress(t.Context()); got != "" {
			t.Fatalf("rejected progress committed store state: %q", got)
		}
		if _, statErr := os.Stat(filepath.Join(f.Dir, "never.txt")); !os.IsNotExist(statErr) {
			t.Fatalf("rejected progress ran gated command: %v", statErr)
		}
	})

	t.Run("profile_without_update_progress", func(t *testing.T) {
		f := newProgressEnvelopeFixture(t, false)
		_, err := f.CallArgs(t, t.Context(), map[string]any{
			"command": "printf 'never\\n' | tee never.txt",
			"progress_update": map[string]any{
				"content": "- [ ] Create never.txt",
			},
		})
		if toolrejection.AsToolReject(err) == nil {
			t.Fatalf("disallowed progress_update err = %v, want policy rejection", err)
		}
		if got := f.Progress(t.Context()); got != "" {
			t.Fatalf("disallowed progress_update committed progress: %q", got)
		}
		if _, statErr := os.Stat(filepath.Join(f.Dir, "never.txt")); !os.IsNotExist(statErr) {
			t.Fatalf("disallowed progress_update ran gated command: %v", statErr)
		}
	})
}

func TestProgramProgressEnvelopeSettlesOnceBeforeGatedCommand(t *testing.T) {
	f := newProgressEnvelopeFixture(t, true)
	raw, err := f.CallArgs(t, t.Context(), map[string]any{
		"command": "printf 'created\\n' | tee result.txt",
		"progress_update": map[string]any{
			"content": "- [ ] Create result.txt",
		},
	})
	testutil.FailErr(t, "run command with progress_update", err)
	var outcome command.ProgramOutcome
	testutil.FailErr(t, "decode program outcome", json.Unmarshal([]byte(raw), &outcome))
	if outcome.Status != 0 || outcome.Refusal != nil {
		t.Fatalf("expected completed command after progress_update: %+v", outcome)
	}
	if outcome.ProgressUpdate == nil || outcome.ProgressUpdate.Status != "updated" || !outcome.ProgressUpdate.DocumentChanged || outcome.ProgressUpdate.Snapshot.Revision == "" {
		t.Fatalf("missing settled progress_update outcome: %+v", outcome.ProgressUpdate)
	}
	if got := f.Progress(t.Context()); got != "- [ ] Create result.txt" {
		t.Fatalf("progress store = %q", got)
	}
	body, err := os.ReadFile(filepath.Join(f.Dir, "result.txt"))
	testutil.FailErr(t, "read gated command output", err)
	if string(body) != "created\n" {
		t.Fatalf("result.txt = %q, want created\\n", body)
	}
	page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, outcome.ProgramID, "", 20)
	testutil.FailErr(t, "read program receipt page", err)
	if page.Progress == nil || page.Progress.State != "committed" || !page.Progress.OwnerInvoked {
		t.Fatalf("expected committed progress receipt: %+v", page.Progress)
	}
	if page.Program.ProgressState != "committed" || page.Program.State != "completed" {
		t.Fatalf("unexpected program receipt state: %+v", page.Program)
	}
	// Progress settlement is terminal and cannot settle a second time.
	if dupErr := f.Recorder.SettleProgramProgress(t.Context(), invocation.ProgressSettlement{
		Identity: toolcontract.ProgramProgressIdentity{
			ProgramID:     outcome.ProgramID,
			ExecutionID:   page.Progress.ExecutionID,
			ActorID:       "fixture-person",
			SessionID:     f.Session,
			RootSessionID: page.Progress.RootSessionID,
		},
		OwnerInvoked:    true,
		State:           "committed",
		DetailReference: "output:duplicate",
	}); dupErr == nil {
		t.Fatal("duplicate progress settlement accepted")
	}
}

func TestProgramProgressEnvelopeSurvivesLaterCommandRefusal(t *testing.T) {
	f := newProgressEnvelopeFixture(t, true)
	// Restrict the profile to read-only command effects while keeping update_progress
	// allowed, so the progress update commits and the subsequent write is refused.
	readOnly, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	testutil.FailErr(t, "compile read-only effects", err)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{
		{ID: "fixture", Tools: map[string]bool{"command": true, "update_progress": true, "write": true, "read": true}, CommandEffects: readOnly},
	})
	policy := toolprofiles.NewProfilePolicyEngine(boundary)
	f.Runtime.Boundary = boundary
	f.Runtime.Executor.Metadata.SetPolicy(policy)

	raw, err := f.CallArgs(t, t.Context(), map[string]any{
		"command": "printf 'blocked\\n' | tee denied.txt",
		"progress_update": map[string]any{
			"content": "- [ ] Attempt denied.txt",
		},
	})
	testutil.FailErr(t, "run refused command with progress_update", err)
	var outcome command.ProgramOutcome
	testutil.FailErr(t, "decode refused program outcome", json.Unmarshal([]byte(raw), &outcome))
	if outcome.Refusal == nil || outcome.State != "rejected" {
		t.Fatalf("expected command refusal after progress commit: %+v", outcome)
	}
	if outcome.ProgressUpdate == nil || outcome.ProgressUpdate.Status != "updated" {
		t.Fatalf("expected committed progress outcome on refused program: %+v", outcome.ProgressUpdate)
	}
	if _, statErr := os.Stat(filepath.Join(f.Dir, "denied.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("refused write created denied.txt: %v", statErr)
	}
	if got := f.Progress(t.Context()); got != "- [ ] Attempt denied.txt" {
		t.Fatalf("later command refusal erased committed progress: %q", got)
	}
	page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, outcome.ProgramID, "", 20)
	testutil.FailErr(t, "read refused program receipt", err)
	if page.Progress == nil || page.Progress.State != "committed" || page.Program.ProgressState != "committed" || page.Program.State != "rejected" {
		t.Fatalf("unexpected receipt state after later refusal: program=%+v progress=%+v", page.Program, page.Progress)
	}
}

func TestProgramProgressSettlementFaultAfterCommit(t *testing.T) {
	f := newProgressEnvelopeFixture(t, true)
	_, err := f.Database.ExecContext(t.Context(), `CREATE TRIGGER fixture_progress_settle_fault BEFORE UPDATE ON invocation_program_progress WHEN NEW.state='committed' BEGIN SELECT RAISE(ABORT,'injected progress settlement fault'); END`)
	testutil.FailErr(t, "install progress settlement fault", err)

	_, err = f.CallArgs(t, t.Context(), map[string]any{
		"command": "printf 'should-not-run\\n' | tee result.txt",
		"progress_update": map[string]any{
			"content": "- [ ] Commit before fault",
		},
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != toolrejection.ToolOwnerFailedCode {
		t.Fatalf("settlement fault err = %v, want TOOL_OWNER_FAILED", err)
	}
	if reject.BeforeEffect {
		t.Fatal("committed progress settlement fault claimed BeforeEffect=true")
	}
	if reject.Data["progress_committed"] != true || reject.Data["command_executed"] != false || reject.Data["program_replay_available"] != false {
		t.Fatalf("unexpected settlement fault data: %+v", reject.Data)
	}
	if _, ok := reject.Data["snapshot"].(progress.RequestSnapshot); !ok {
		t.Fatalf("settlement fault omitted committed progress snapshot: %+v", reject.Data)
	}
	if got := f.Progress(t.Context()); got != "- [ ] Commit before fault" {
		t.Fatalf("committed progress lost after settlement fault: %q", got)
	}
	if _, statErr := os.Stat(filepath.Join(f.Dir, "result.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("gated command ran after progress settlement fault: %v", statErr)
	}
	programID, _ := reject.Data["program_id"].(string)
	if programID == "" {
		t.Fatalf("settlement fault missing program_id: %+v", reject.Data)
	}
	page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, programID, "", 20)
	testutil.FailErr(t, "read recovered program page", err)
	if page.Program.State != "interrupted" || page.Program.StopCode != toolrejection.ToolOwnerInterruptedCode || page.Program.ProgressState != "unknown" {
		t.Fatalf("parent program was not terminally reconciled: %+v", page.Program)
	}
	if page.Progress == nil || page.Progress.State != "unknown" {
		t.Fatalf("progress receipt was not reconciled to unknown: %+v", page.Progress)
	}
	// Subsequent command in the same session reuses the committed progress without duplicating it.
	raw, err := f.Call(t, t.Context(), "printf 'reused\\n' | tee reused.txt")
	testutil.FailErr(t, "reuse session after settlement fault", err)
	var reused command.ProgramOutcome
	testutil.FailErr(t, "decode reused outcome", json.Unmarshal([]byte(raw), &reused))
	if reused.Status != 0 || reused.Refusal != nil {
		t.Fatalf("session reuse failed: %+v", reused)
	}
	body, err := os.ReadFile(filepath.Join(f.Dir, "reused.txt"))
	testutil.FailErr(t, "read reused.txt", err)
	if string(body) != "reused\n" {
		t.Fatalf("reused.txt = %q", body)
	}
	if got := f.Progress(t.Context()); got != "- [ ] Commit before fault" {
		t.Fatalf("reused command altered committed progress: %q", got)
	}
}

func TestProgramProgressPrecommitRejectionWithSettlementFault(t *testing.T) {
	f := newProgressEnvelopeFixture(t, false)
	_, err := f.Database.ExecContext(t.Context(), `CREATE TRIGGER fixture_refusal_fault BEFORE UPDATE ON invocation_programs WHEN NEW.progress_state='rejected' OR NEW.state='rejected' BEGIN SELECT RAISE(ABORT,'injected refusal settlement fault'); END`)
	testutil.FailErr(t, "install refusal settlement fault", err)

	_, err = f.CallArgs(t, t.Context(), map[string]any{
		"command": "printf 'never\\n' | tee never.txt",
		"progress_update": map[string]any{
			"content": "- [ ] Blocked progress",
		},
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) {
		t.Fatalf("precommit rejection with settlement fault err = %v, want ToolReject", err)
	}
	if reject.Data["progress_committed"] != false || reject.Data["command_executed"] != false || reject.Data["program_replay_available"] != false {
		t.Fatalf("unexpected precommit fault data: %+v", reject.Data)
	}
	if reason, _ := reject.Data["settlement_error"].(string); reason == "" {
		t.Fatalf("settlement_error omitted on refusal fault: %+v", reject.Data)
	}
	if got := f.Progress(t.Context()); got != "" {
		t.Fatalf("precommit rejection committed progress: %q", got)
	}
	if _, statErr := os.Stat(filepath.Join(f.Dir, "never.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("precommit rejection ran command: %v", statErr)
	}
	programID, _ := reject.Data["program_id"].(string)
	page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, programID, "", 20)
	testutil.FailErr(t, "read recovered rejected program page", err)
	if page.Program.State != "interrupted" || page.Program.StopCode != toolrejection.ToolOwnerInterruptedCode {
		t.Fatalf("fallback recovery did not settle parent program: %+v", page.Program)
	}
	_, err = f.Database.ExecContext(t.Context(), `DROP TRIGGER fixture_refusal_fault`)
	testutil.FailErr(t, "drop refusal settlement fault", err)
	raw, err := f.Call(t, t.Context(), "printf 'ok'")
	testutil.FailErr(t, "reuse session after refusal fault", err)
	var reused command.ProgramOutcome
	testutil.FailErr(t, "decode reused outcome", json.Unmarshal([]byte(raw), &reused))
	if reused.Status != 0 || reused.Stdout != "ok" {
		t.Fatalf("session reuse after refusal fault failed: %+v", reused)
	}
}

func TestProgramProgressRejectedOnSurfaceWithoutStandaloneProgress(t *testing.T) {
	f := newProgressEnvelopeFixture(t, true)
	f.Configure = func(tc *tools.ToolContext) {
		tc.Turn.TurnSurfaceID = "await_user"
	}
	_, err := f.CallArgs(t, t.Context(), map[string]any{
		"command": "printf 'never\\n' | tee never.txt",
		"progress_update": map[string]any{
			"content": "- [ ] Should be denied on await_user",
		},
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "TOOL_NOT_OFFERED" {
		t.Fatalf("surface-restricted progress_update err = %v, want TOOL_NOT_OFFERED", err)
	}
	if got := f.Progress(t.Context()); got != "" {
		t.Fatalf("surface-restricted progress_update committed progress: %q", got)
	}
	if _, statErr := os.Stat(filepath.Join(f.Dir, "never.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("surface-restricted progress_update ran command: %v", statErr)
	}
}
