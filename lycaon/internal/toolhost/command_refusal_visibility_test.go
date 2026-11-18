package toolhost

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestComposedCommandRefusalsFollowRecordedAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, tool, source, code string
		needsChecklist           bool
		wantAdmission            bool
		wantEffects              bool
	}{
		{name: "redirect replace", source: "cat <<'EOF' > target\ntext\nEOF", code: "PROGRESS_MISSING", needsChecklist: true},
		{name: "redirect append", source: "printf 'text' >> target", code: "PROGRESS_MISSING", needsChecklist: true},
		{name: "survey outside roots", tool: "survey_repo", code: "SURVEY_PATH_ESCAPE"},
		{name: "redirect refused after read", source: "ls; printf text > target", code: "PROGRESS_MISSING", needsChecklist: true, wantAdmission: true},
		{name: "survey refused after write", source: "printf text > target; repo-map --path ../outside", code: "SURVEY_PATH_ESCAPE", wantAdmission: true, wantEffects: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newComposedProgramFixture(t)
			if tc.needsChecklist {
				testutil.FailErr(t, "clear progress", f.SetProgress(""))
			}
			out := &tools.ToolInvocationOut{}
			var progress []api.CommandProgramReceipt
			f.Configure = func(call *tools.ToolContext) {
				call.Effects.Out = out
				call.Effects.ReportProgress = func(update api.ToolProgress) {
					if receipt := update.CommandProgram; receipt != nil {
						progress = append(progress, *receipt)
					}
				}
			}
			var raw string
			var err error
			if tc.tool == "survey_repo" {
				handler := f.Runtime.Registry.Run
				raw, err = f.Invoke(t, t.Context(), tc.tool, map[string]any{"path": "../outside", "bundle": "layout_overview"},
					func(ctx context.Context, args map[string]any, call tools.ToolContext) (string, error) {
						return handler(ctx, tc.tool, args, call)
					})
			} else {
				raw, err = f.Call(t, t.Context(), tc.source)
			}
			testutil.FailErr(t, "invoke fixture program", err)
			result := decodeProgramOutcome(t, raw)
			refusal := out.ProgramRefusal
			if result.State != "rejected" || result.Refusal == nil || result.Refusal.Code != tc.code ||
				refusal == nil || refusal.Code != tc.code || refusal.BeforeAdmission == tc.wantAdmission {
				t.Fatalf("outcome=%+v, refusal=%+v, visibility facts=%+v", result, result.Refusal, refusal)
			}
			if out.ProgramProgressPublished() != tc.wantAdmission || (len(progress) != 0) != tc.wantAdmission {
				t.Fatalf("published latch=%v, frames=%d, want activity=%v", out.ProgramProgressPublished(), len(progress), tc.wantAdmission)
			}
			for _, frame := range progress {
				if len(frame.Activities) == 0 {
					t.Fatalf("published an empty progress receipt: %+v", frame)
				}
			}
			var started, refused int
			err = f.Database.QueryRowContext(t.Context(), `SELECT COUNT(*) FILTER (WHERE owner_started=1), COUNT(*) FILTER (WHERE state='refused') FROM invocation_program_executions WHERE program_id=?`, result.ProgramID).Scan(&started, &refused)
			testutil.FailErr(t, "read child receipts", err)
			if (started > 0) != tc.wantAdmission || refused != 1 {
				t.Fatalf("recorded %d starts and %d refusals", started, refused)
			}
			if (result.CompletedEffects > 0) != tc.wantEffects || result.UncertainEffects != 0 {
				t.Fatalf("unexpected effects: %+v", result)
			}
			data, err := os.ReadFile(filepath.Join(f.Dir, "target"))
			if tc.wantEffects {
				testutil.FailErr(t, "read committed target", err)
				if string(data) != "text" {
					t.Fatalf("committed target = %q", data)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("refused operation altered target: %q, %v", data, err)
			}
		})
	}
}

func TestScratchRedirectDoesNotRequireProjectChecklist(t *testing.T) {
	f := newComposedProgramFixture(t)
	testutil.FailErr(t, "clear progress", f.SetProgress(""))
	outcome := callComposedProgram(t, f, "printf '%s' fixture > @scratch/target")
	if outcome.Status != 0 || outcome.Refusal != nil {
		t.Fatalf("scratch redirect = %+v, refusal=%+v", outcome, outcome.Refusal)
	}
	data, err := os.ReadFile(filepath.Join(f.ScratchDir, "target"))
	testutil.FailErr(t, "read scratch redirect", err)
	if string(data) != "fixture" {
		t.Fatalf("scratch redirect = %q", data)
	}
}
