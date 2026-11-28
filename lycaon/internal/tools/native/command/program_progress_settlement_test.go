package command_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

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
	if !errors.As(err, &reject) || reject.Code != "TOOL_DENIED" {
		t.Fatalf("surface-restricted progress_update err = %v, want TOOL_DENIED", err)
	}
	if got := f.Progress(t.Context()); got != "" {
		t.Fatalf("surface-restricted progress_update committed progress: %q", got)
	}
	if _, statErr := os.Stat(filepath.Join(f.Dir, "never.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("surface-restricted progress_update ran command: %v", statErr)
	}
}
