package native_test

import (
    "encoding/json"
    "testing"
    "github.com/lycaon/lycaon/internal/programtest"
    "github.com/lycaon/lycaon/internal/testutil"
    "github.com/lycaon/lycaon/internal/toolrejection"
    "github.com/lycaon/lycaon/internal/tools"
    "github.com/lycaon/lycaon/internal/tools/native"
    "github.com/lycaon/lycaon/internal/tools/native/command"
    "github.com/lycaon/lycaon/pkg/api"
)

func TestVerifyRunsSelectedBashProgramWithOwnReceipt(t *testing.T) {
    f := programtest.New(t, programtest.Options{Tools: []string{"verify"}})
    tool := &native.VerifyTool{Programs: f.Runtime, DeclaredCommand: func(string) string { return "value=$(printf checked); printf '%s' \"$value\"" }}
    out := &tools.ToolInvocationOut{}
    f.Configure = func(tc *tools.ToolContext) { tc.Effects.Out = out }
    raw, err := f.Invoke(t, t.Context(), "verify", map[string]any{}, tool.Run)
    testutil.FailErr(t, "selected program", err)
    var result command.ProgramOutcome
    testutil.FailErr(t, "decode program", json.Unmarshal([]byte(raw), &result))
    if result.Status != 0 || result.Stdout != "checked" || out.SourceRun == nil || !out.SourceRun.IsCheck || out.SourceRun.Verdict != api.SourceVerdictPassed {
        t.Fatalf("result=%s source=%+v", raw, out.SourceRun)
    }
    var name string
    testutil.FailErr(t, "parent tool identity", f.Database.QueryRowContext(t.Context(), "SELECT tool_name FROM invocation_receipts WHERE id = (SELECT parent_receipt_id FROM invocation_programs LIMIT 1)").Scan(&name))
    if name != "verify" { t.Fatalf("initiating tool=%q", name) }
}

func TestVerifyRejectsSubstituteBeforeProgramStarts(t *testing.T) {
    f := programtest.New(t, programtest.Options{Tools: []string{"verify"}})
    tool := &native.VerifyTool{Programs: f.Runtime, DeclaredCommand: func(string) string { return "true" }}
    _, err := f.Invoke(t, t.Context(), "verify", map[string]any{"command":"false"}, tool.Run)
    reject := toolrejection.AsToolReject(err)
    if reject == nil || reject.Code != "VERIFY_DECLARED_COMMAND_OVERRIDE" || len(f.Programs(t)) != 0 { t.Fatalf("programs=%v error=%v", f.Programs(t), err) }
}

func TestVerifyWithoutSelectedCheckNeedsSource(t *testing.T) {
    tool := &native.VerifyTool{}
    _, err := tool.Run(t.Context(), map[string]any{}, tools.ToolContext{})
    if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "VERIFY_COMMAND_UNDECLARED" { t.Fatalf("error=%v", err) }
}
