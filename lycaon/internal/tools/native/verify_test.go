package native_test

import (
	"encoding/json"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
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
	if result.ResultVersion != 2 || result.Outcome != "passed" || result.Status != 0 || result.Stdout != "checked" || out.SourceRun == nil || !out.SourceRun.IsCheck || out.SourceRun.Verdict != api.SourceVerdictPassed {
		t.Fatalf("result=%s source=%+v", raw, out.SourceRun)
	}
	recovered, err := f.Runtime.ReceiptResult(t.Context(), f.Session, result.ProgramID)
	testutil.FailErr(t, "recover program result", err)
	var receipt command.ProgramOutcome
	testutil.FailErr(t, "decode recovery", json.Unmarshal([]byte(recovered), &receipt))
	if receipt.ResultVersion != 2 || receipt.Outcome != "" {
		t.Fatalf("recovery version or invented verdict: %s", recovered)
	}
	var name string
	testutil.FailErr(t, "parent tool identity", f.Database.QueryRowContext(t.Context(), "SELECT tool_name FROM invocation_receipts WHERE id = (SELECT parent_receipt_id FROM invocation_programs LIMIT 1)").Scan(&name))
	if name != "verify" {
		t.Fatalf("initiating tool=%q", name)
	}
}

func TestVerifyRejectsSubstituteBeforeProgramStarts(t *testing.T) {
	f := programtest.New(t, programtest.Options{Tools: []string{"verify"}})
	tool := &native.VerifyTool{Programs: f.Runtime, DeclaredCommand: func(string) string { return "true" }}
	_, err := f.Invoke(t, t.Context(), "verify", map[string]any{"command": "false"}, tool.Run)
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "VERIFY_DECLARED_COMMAND_OVERRIDE" || len(f.Programs(t)) != 0 {
		t.Fatalf("programs=%v error=%v", f.Programs(t), err)
	}
}

func TestVerifyWithoutSelectedCheckNeedsSource(t *testing.T) {
	tool := &native.VerifyTool{}
	_, err := tool.Run(t.Context(), map[string]any{}, tools.ToolContext{})
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "VERIFY_COMMAND_UNDECLARED" {
		t.Fatalf("error=%v", err)
	}
}

func TestVerifyExplicitSelectedBashSource(t *testing.T) {
	f := programtest.New(t, programtest.Options{Tools: []string{"verify"}})
	source := "value=$(printf checked); printf '%s' \"$value\""
	tool := &native.VerifyTool{Programs: f.Runtime, DeclaredCommand: func(string) string { return source }}
	raw, err := f.Invoke(t, t.Context(), "verify", map[string]any{"command": source}, tool.Run)
	testutil.FailErr(t, "selected Bash source", err)
	var result command.ProgramOutcome
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(raw), &result))
	if result.Outcome != "passed" || result.Stdout != "checked" {
		t.Fatalf("result=%s", raw)
	}
}

func TestVerifyFailedProgramProducesFailedCheck(t *testing.T) {
	f := programtest.New(t, programtest.Options{Tools: []string{"verify"}})
	tool := &native.VerifyTool{Programs: f.Runtime}
	out := &tools.ToolInvocationOut{}
	f.Configure = func(tc *tools.ToolContext) { tc.Effects.Out = out }
	raw, err := f.Invoke(t, t.Context(), "verify", map[string]any{"command": "false"}, tool.Run)
	testutil.FailErr(t, "failed check execution", err)
	var result command.ProgramOutcome
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(raw), &result))
	if result.Status != 1 || result.Outcome != "failed" || out.SourceRun == nil || out.SourceRun.Verdict != api.SourceVerdictFailed {
		t.Fatalf("result=%s source=%+v", raw, out.SourceRun)
	}
}

func TestVerifyRunningJobsHaveNoVerdict(t *testing.T) {
	f := programtest.New(t, programtest.Options{Tools: []string{"verify"}})
	out := &tools.ToolInvocationOut{}
	f.Configure = func(tc *tools.ToolContext) { tc.Effects.Out = out }
	raw, err := f.Invoke(t, t.Context(), "verify", map[string]any{"command": "sleep 5 &"}, (&native.VerifyTool{Programs: f.Runtime}).Run)
	testutil.FailErr(t, "start check job", err)
	var result command.ProgramOutcome
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(raw), &result))
	if result.Outcome != "" || out.SourceRun != nil {
		t.Fatalf("unsettled check acquired verdict: %s %+v", raw, out.SourceRun)
	}
}

func TestVerifyRefusedProgramIsUnverifiable(t *testing.T) {
	f := programtest.New(t, programtest.Options{Tools: []string{"verify"}})
	f.Runtime.ExternalHidden = true
	out := &tools.ToolInvocationOut{}
	f.Configure = func(tc *tools.ToolContext) { tc.Effects.Out = out }
	raw, err := f.Invoke(t, t.Context(), "verify", map[string]any{"command": "external -- /bin/echo never"}, (&native.VerifyTool{Programs: f.Runtime}).Run)
	testutil.FailErr(t, "refused check", err)
	var result command.ProgramOutcome
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(raw), &result))
	if result.Outcome != "unverifiable" || out.SourceRun == nil || out.SourceRun.Verdict != api.SourceVerdictUnverifiable {
		t.Fatalf("refused check: %s %+v", raw, out.SourceRun)
	}
}

func TestVerifyBindsEvidenceToSourceBeforeExecution(t *testing.T) {
	f := programtest.New(t, programtest.Options{Tools: []string{"verify"}})
	ledger := sourceledger.New(f.Database, t.TempDir())
	out := &tools.ToolInvocationOut{}
	f.Configure = func(tc *tools.ToolContext) {
		tc.Source.Observations = ledger.Inventory
		tc.Effects.Out = out
	}
	revision, root := sourceledger.VerificationState(t.Context(), ledger.Inventory, f.Dir)
	if revision == "" || root == "" {
		t.Fatal("fixture source identity unavailable")
	}
	_, err := f.Invoke(t, t.Context(), "verify", map[string]any{"command": "true"}, (&native.VerifyTool{Programs: f.Runtime}).Run)
	testutil.FailErr(t, "source-bound check", err)
	if out.SourceRun == nil || out.SourceRun.SourceRevision != revision || out.SourceRun.SourceRootDigest != root {
		t.Fatalf("source identity=%+v want %s %s", out.SourceRun, revision, root)
	}
}
