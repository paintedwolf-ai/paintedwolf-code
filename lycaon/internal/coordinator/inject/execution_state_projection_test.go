package inject

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

type receiptSpec struct {
	batch, call, tool, digest, code, verdict, revision, root string
	status                                                   api.InvocationStatus
	args                                                     map[string]any
	running                                                  bool
}

func receiptMessage(spec receiptSpec) api.Message {
	status := spec.status
	if status == "" {
		status = api.InvocationStatusCompleted
		if spec.code != "" {
			status = api.InvocationStatusRejected
		}
	}
	receipt := &api.InvocationReceipt{ID: "receipt-" + spec.call, Tool: spec.tool, ToolCallID: spec.call, ArgsDigest: spec.digest, Status: status,
		Invoked: status == api.InvocationStatusCompleted, SourceVerdict: spec.verdict, SourceRevision: spec.revision, SourceRootDigest: spec.root}
	if spec.code != "" {
		receipt.Failure = &api.InvocationFailure{Code: spec.code}
	}
	result := &api.ToolResult{Tool: spec.tool, ToolCallID: spec.call, AssistantMessageID: spec.batch, Invocation: receipt, ToolArgs: spec.args}
	if spec.running {
		result.Process = &api.ToolProcessHandle{Handle: "job", Running: true}
	}
	return api.Message{Role: api.MessageRoleTool, ToolResult: result}
}

// settledBatches returns n unrelated successful read batches.
func settledBatches(prefix string, n int) []api.Message {
	var out []api.Message
	for i := range n {
		id := fmt.Sprintf("%s-%d", prefix, i)
		out = append(out, receiptMessage(receiptSpec{batch: id, call: "call-" + id, tool: "read", digest: id}))
	}
	return out
}

func rejectedWrite(call, path string) api.Message {
	return receiptMessage(receiptSpec{batch: "batch-" + call, call: call, tool: "write", digest: "digest-" + call, code: "MUTATION_BROKE_PARSE", args: map[string]any{"path": path}})
}

func rowCalls(rows []executionStateRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.CallID)
	}
	return out
}

func sameCalls(rows []executionStateRow, want ...string) bool {
	got := rowCalls(rows)
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func retiredState(p executionStateProjection, call string) (reminderState, bool) {
	for _, row := range p.Retired {
		if row.CallID == call {
			return row.State, true
		}
	}
	return reminderLive, false
}

func TestReminderAgesAndExpiry_ACEXEC03(t *testing.T) {
	for age := range MaxRejectedOperationStepAge + 2 {
		t.Run(fmt.Sprintf("age %d", age), func(t *testing.T) {
			history := append([]api.Message{rejectedWrite("failed", "a.go")}, settledBatches("later", age)...)
			before, _ := json.Marshal(history)
			got := projectExecutionState(history, "")
			if age < MaxRejectedOperationStepAge {
				if !sameCalls(got.Operations, "failed") || got.Operations[0].Age != age {
					t.Fatalf("live row at age %d: %+v", age, got.Operations)
				}
				if vars := executionStateVars(history, ""); vars == nil {
					t.Fatal("live reminder produced no block")
				}
			} else {
				if len(got.Operations) != 0 || got.OperationsOmitted != 0 {
					t.Fatalf("expired row stayed: %+v", got)
				}
				if state, ok := retiredState(got, "failed"); !ok || state != reminderExpired {
					t.Fatalf("expiry not recorded as expired: %+v", got.Retired)
				}
				if vars := executionStateVars(history, ""); vars != nil {
					t.Fatalf("expired reminder still rendered: %#v", vars)
				}
			}
			after, _ := json.Marshal(history)
			if string(before) != string(after) {
				t.Fatal("projection changed the durable receipts")
			}
		})
	}
}

func TestFailedExecutionAgesLikeRejectedOperation_ACEXEC03(t *testing.T) {
	failed := receiptMessage(receiptSpec{batch: "start", call: "failed", tool: "command", digest: "commit", verdict: api.SourceVerdictFailed})
	for age := range MaxRejectedOperationStepAge + 1 {
		got := projectExecutionState(append([]api.Message{failed}, settledBatches("later", age)...), "")
		if live := age < MaxRejectedOperationStepAge; got.Recovery != live || (len(got.Executions) == 1) != live {
			t.Fatalf("age %d: recovery=%v rows=%v", age, got.Recovery, rowCalls(got.Executions))
		}
	}
}

func TestBatchCountsOnceAndPollsDoNotAge_ACEXEC03(t *testing.T) {
	failed := rejectedWrite("failed", "a.go")
	var parallel []api.Message
	for i := range 6 {
		spec := receiptSpec{batch: "mixed", call: fmt.Sprintf("mixed-%d", i), tool: "read", digest: fmt.Sprint(i)}
		if i%2 == 0 {
			spec.code = "READ_FAILED"
		}
		parallel = append(parallel, receiptMessage(spec))
	}
	poll := func(batch, tool string) api.Message {
		return receiptMessage(receiptSpec{batch: batch, call: "call-" + batch, tool: tool, digest: batch})
	}
	cases := []struct {
		name  string
		later []api.Message
		age   int
	}{
		{"parallel mixed batch counts once", parallel, 1},
		{"rejected batch still counts", []api.Message{rejectedWrite("other", "b.go")}, 1},
		{"command output", []api.Message{poll("p1", "command_output")}, 0},
		{"terminal read", []api.Message{poll("p2", "terminal_read")}, 0},
		{"terminal snapshot", []api.Message{poll("p3", "terminal_snapshot")}, 0},
		{"held result", []api.Message{poll("p4", "held_result")}, 0},
		{"wait", []api.Message{poll("p5", "wait")}, 0},
		{"poll plus action counts", []api.Message{poll("p6", "command_output"), poll("p6", "read")}, 1},
		{"returned while running", []api.Message{receiptMessage(receiptSpec{batch: "live", call: "live", tool: "command", digest: "live", running: true})}, 0},
		{"unsettled receipt", []api.Message{receiptMessage(receiptSpec{batch: "open", call: "open", tool: "read", digest: "open", status: api.InvocationStatusRunning})}, 0},
		{"many polls between batches", append(append(settledBatches("x", 1), poll("p7", "command_output"), poll("p8", "command_output")), settledBatches("y", 1)...), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := projectExecutionState(append([]api.Message{failed}, tc.later...), "")
			if len(got.Operations) == 0 || got.Operations[0].CallID != "failed" || got.Operations[0].Age != tc.age {
				t.Fatalf("want age %d: %+v", tc.age, got.Operations)
			}
		})
	}
}

func TestRepeatedRejectionRefreshesItsOwnReminder_ACEXEC03(t *testing.T) {
	first := receiptMessage(receiptSpec{batch: "one", call: "first", tool: "write", digest: "same", code: "MUTATION_BROKE_PARSE", args: map[string]any{"path": "a.go"}})
	repeat := receiptMessage(receiptSpec{batch: "four", call: "repeat", tool: "write", digest: "same", code: "MUTATION_BROKE_PARSE", args: map[string]any{"path": "a.go"}})
	older := receiptMessage(receiptSpec{batch: "one", call: "older", tool: "write", digest: "other", code: "MUTATION_BROKE_PARSE", args: map[string]any{"path": "b.go"}})
	history := append([]api.Message{older, first}, settledBatches("mid", 2)...)
	history = append(history, repeat)
	got := projectExecutionState(history, "")
	if !sameCalls(got.Operations, "older", "repeat") || got.Operations[1].Age != 0 || got.Operations[0].Age != 3 {
		t.Fatalf("repeat did not refresh only its own row: %+v", got.Operations)
	}
	if first.ToolResult.Invocation.Status != api.InvocationStatusRejected {
		t.Fatal("refresh altered the original outcome")
	}
	got = projectExecutionState(append(history, settledBatches("end", 1)...), "")
	if !sameCalls(got.Operations, "repeat") {
		t.Fatalf("continued activity did not age the older reminder: %+v", got.Operations)
	}
}

func TestOmittedCountsLiveRowsOnly_PS06(t *testing.T) {
	var history []api.Message
	history = append(history, rejectedWrite("expired", "0.go"))
	history = append(history, settledBatches("gap", 3)...)
	for i := 1; i <= 5; i++ {
		history = append(history, receiptMessage(receiptSpec{batch: "burst", call: fmt.Sprintf("live-%d", i), tool: "write", digest: fmt.Sprint(i), code: "MUTATION_BROKE_PARSE", args: map[string]any{"path": fmt.Sprintf("%d.go", i)}}))
	}
	got := projectExecutionState(history, "")
	if !sameCalls(got.Operations, "live-3", "live-4", "live-5") || got.OperationsOmitted != 2 {
		t.Fatalf("rows=%v omitted=%d", rowCalls(got.Operations), got.OperationsOmitted)
	}
	if state, _ := retiredState(got, "expired"); state != reminderExpired {
		t.Fatalf("expired row counted live: %+v", got.Retired)
	}
}

func TestOnlyLinkedEvidenceResolves_ACEXEC01(t *testing.T) {
	first, second := rejectedWrite("first", "a.go"), rejectedWrite("second", "b.go")
	changed := receiptMessage(receiptSpec{batch: "fix", call: "fix", tool: "edit", digest: "different", args: map[string]any{"path": "a.go"}})
	got := projectExecutionState([]api.Message{first, second, changed}, "")
	if !sameCalls(got.Operations, "second") {
		t.Fatalf("changed arguments on the linked path should resolve only that row: %v", rowCalls(got.Operations))
	}
	if state, ok := retiredState(got, "first"); !ok || state != reminderResolved {
		t.Fatalf("resolved row not distinguished from expiry: %+v", got.Retired)
	}

	check := func(call string, status api.InvocationStatus, code, verdict, revision, root, command, cwd string) api.Message {
		return receiptMessage(receiptSpec{batch: "batch-" + call, call: call, tool: "verify", digest: "digest-" + call, status: status, code: code, verdict: verdict, revision: revision, root: root,
			args: map[string]any{"command": command, "cwd": cwd}})
	}
	failing := check("failing", api.InvocationStatusCompleted, "", api.SourceVerdictFailed, "boot:1", "root", "project-check", ".")
	cases := []struct {
		name     string
		later    []api.Message
		resolved bool
	}{
		{"same check root and revision", []api.Message{check("pass", "", "", api.SourceVerdictPassed, "boot:1", "root", "project-check", ".")}, true},
		{"same check after a stamped change", []api.Message{
			receiptMessage(receiptSpec{batch: "w", call: "w", tool: "write", digest: "w", revision: "boot:2", root: "root", args: map[string]any{"path": "a.go"}}),
			check("pass", "", "", api.SourceVerdictPassed, "boot:2", "root", "project-check", "."),
		}, true},
		{"different command", []api.Message{check("pass", "", "", api.SourceVerdictPassed, "boot:1", "root", "other-check", ".")}, false},
		{"different cwd", []api.Message{check("pass", "", "", api.SourceVerdictPassed, "boot:1", "root", "project-check", "sub")}, false},
		{"different root", []api.Message{check("pass", "", "", api.SourceVerdictPassed, "boot:1", "elsewhere", "project-check", ".")}, false},
		{"stale revision", []api.Message{
			receiptMessage(receiptSpec{batch: "w", call: "w", tool: "write", digest: "w", revision: "boot:2", root: "root", args: map[string]any{"path": "a.go"}}),
			check("pass", "", "", api.SourceVerdictPassed, "boot:1", "root", "project-check", "."),
		}, false},
		{"no revision", []api.Message{check("pass", "", "", api.SourceVerdictPassed, "", "root", "project-check", ".")}, false},
		{"failed again", []api.Message{check("pass", "", "", api.SourceVerdictFailed, "boot:1", "root", "project-check", ".")}, false},
		{"plain command with a passing verdict", []api.Message{receiptMessage(receiptSpec{batch: "c", call: "c", tool: "command", digest: "c", verdict: api.SourceVerdictPassed, revision: "boot:1", root: "root", args: map[string]any{"command": "project-check", "cwd": "."}})}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := projectExecutionState(append([]api.Message{failing}, tc.later...), "project-check")
			state, ok := retiredState(got, "failing")
			if resolved := ok && state == reminderResolved; resolved != tc.resolved {
				t.Fatalf("resolved=%v want %v: %+v", resolved, tc.resolved, got)
			}
			if tc.resolved && got.Recovery {
				t.Fatal("resolved failure kept the block alive")
			}
		})
	}

	t.Run("declared check runs through an empty verify command", func(t *testing.T) {
		pass := check("pass", "", "", api.SourceVerdictPassed, "boot:1", "root", "", ".")
		got := projectExecutionState([]api.Message{failing, pass}, "project-check")
		if got.Recovery {
			t.Fatalf("declared check did not link: %+v", got)
		}
	})
	t.Run("later change and restart do not bring a resolved row back", func(t *testing.T) {
		pass := check("pass", "", "", api.SourceVerdictPassed, "boot:1", "root", "project-check", ".")
		restarted := receiptMessage(receiptSpec{batch: "after", call: "after", tool: "write", digest: "after", revision: "other-boot:1", root: "root", args: map[string]any{"path": "a.go"}})
		got := projectExecutionState([]api.Message{failing, pass, restarted}, "project-check")
		if got.Recovery {
			t.Fatalf("resolved reminder returned: %+v", got)
		}
	})
	t.Run("identical retry resolves", func(t *testing.T) {
		rejected := receiptMessage(receiptSpec{batch: "a", call: "a", tool: "command", digest: "same", code: "COMMAND_NOT_FOUND"})
		retry := receiptMessage(receiptSpec{batch: "b", call: "b", tool: "command", digest: "same", verdict: api.SourceVerdictPassed})
		if got := projectExecutionState([]api.Message{rejected, retry}, ""); got.Recovery {
			t.Fatalf("identical retry left the reminder: %+v", got)
		}
	})
}

func TestUnrelatedSuccessNeverProvesRecovery_ACEXEC02(t *testing.T) {
	cmd := func(batch, digest string, verdict string, command, cwd string) api.Message {
		return receiptMessage(receiptSpec{batch: batch, call: batch, tool: "command", digest: digest, verdict: verdict, revision: "boot:1", root: "root", args: map[string]any{"command": command, "cwd": cwd}})
	}
	cases := []struct {
		name              string
		failed, succeeded string
	}{
		{"commit then status", "git commit -m change", "git status"},
		{"tests then task listing", "./task test", "./task --list"},
		{"git -C form", "git -C sub commit -m change", "git -C sub status"},
		{"wrapper form", "bash -c 'git commit -m change'", "bash -c 'git status'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			history := []api.Message{cmd("failed", "f", api.SourceVerdictFailed, tc.failed, "."), cmd("ok", "o", api.SourceVerdictPassed, tc.succeeded, ".")}
			for age := 1; age <= MaxRejectedOperationStepAge; age++ {
				got := projectExecutionState(history, "")
				live := age < MaxRejectedOperationStepAge
				if got.Recovery != live {
					t.Fatalf("age %d: recovery=%v", age, got.Recovery)
				}
				if _, resolved := retiredState(got, "failed"); resolved != !live {
					t.Fatalf("age %d: retired=%+v", age, got.Retired)
				}
				if live && (got.Executions[0].CallID != "failed" || got.Executions[0].Age != age) {
					t.Fatalf("age %d: failed receipt hidden or misaged: %+v", age, got.Executions)
				}
				history = append(history, cmd(fmt.Sprintf("ok-%d", age), fmt.Sprintf("o-%d", age), api.SourceVerdictPassed, tc.succeeded, "."))
			}
		})
	}
	t.Run("unrelated successful change keeps a pathless rejection", func(t *testing.T) {
		pathless := receiptMessage(receiptSpec{batch: "a", call: "a", tool: "write", digest: "a", code: "TOOL_ARGS_INVALID"})
		other := receiptMessage(receiptSpec{batch: "b", call: "b", tool: "write", digest: "b", args: map[string]any{"path": "other.go"}})
		if got := projectExecutionState([]api.Message{pathless, other}, ""); !sameCalls(got.Operations, "a") {
			t.Fatalf("unrelated change resolved a pathless rejection: %+v", got.Operations)
		}
	})
}

func TestExecutionRowCapKeepsLiveFailures_ACEXEC02(t *testing.T) {
	history := []api.Message{receiptMessage(receiptSpec{batch: "failed", call: "failed", tool: "command", digest: "f", verdict: api.SourceVerdictFailed})}
	for i := range 3 {
		id := fmt.Sprintf("ok-%d", i)
		history = append(history, receiptMessage(receiptSpec{batch: id, call: id, tool: "command", digest: id, verdict: api.SourceVerdictPassed}))
	}
	got := projectExecutionState(history, "")
	if !sameCalls(got.Executions, "failed", "ok-1", "ok-2") || got.ExecutionsOmitted != 1 || !got.Recovery {
		t.Fatalf("cap hid a live failure: %v omitted=%d", rowCalls(got.Executions), got.ExecutionsOmitted)
	}
}

func TestPollVerdictsSayNothingWithoutOne(t *testing.T) {
	rejected := receiptMessage(receiptSpec{batch: "a", call: "a", tool: "command", digest: "a", code: "COMMAND_NOT_FOUND"})
	silent := receiptMessage(receiptSpec{batch: "p", call: "p", tool: "command_output", digest: "p", code: "NOT_FOUND"})
	got := projectExecutionState([]api.Message{silent}, "")
	if got.Recovery {
		t.Fatalf("poll without a verdict raised a reminder: %+v", got)
	}
	passed := receiptMessage(receiptSpec{batch: "q", call: "q", tool: "command_output", digest: "q", verdict: api.SourceVerdictPassed})
	if got := projectExecutionState([]api.Message{rejected, passed}, ""); !got.Recovery {
		t.Fatalf("unlinked poll verdict resolved a failure: %+v", got)
	}
	failedPoll := receiptMessage(receiptSpec{batch: "r", call: "r", tool: "command_output", digest: "r", verdict: api.SourceVerdictFailed})
	if got := projectExecutionState([]api.Message{failedPoll}, ""); !got.Recovery {
		t.Fatalf("failed poll verdict was ignored: %+v", got)
	}
}

func TestRejectedCommandProgramProjectsRejectionAndChecklistPrerequisite(t *testing.T) {
	rejectedCmd := receiptMessage(receiptSpec{
		batch:  "b1",
		call:   "cmd-1",
		tool:   "command",
		digest: "mkdir-game",
		status: api.InvocationStatusCompleted,
		args:   map[string]any{"command": "mkdir -p tic-tac-toe"},
	})
	rejectedCmd.ToolResult.CommandProgram = &api.CommandProgramReceipt{
		ID:              "prog-1",
		ParentReceiptID: rejectedCmd.ToolResult.Invocation.ID,
		State:           string(api.InvocationStatusRejected),
		StopCode:        "PROGRESS_MISSING",
	}

	got := projectExecutionState([]api.Message{rejectedCmd}, "")
	if !got.Recovery || len(got.Operations) != 1 || got.Operations[0].Status != string(api.InvocationStatusRejected) || got.Operations[0].Code != "PROGRESS_MISSING" {
		t.Fatalf("expected rejected command program in operations and recovery: %+v", got)
	}
	if len(got.Executions) != 1 || got.Executions[0].Status != string(api.InvocationStatusRejected) || got.Executions[0].Code != "PROGRESS_MISSING" {
		t.Fatalf("expected rejected command program in executions: %+v", got.Executions)
	}

	progressDone := receiptMessage(receiptSpec{
		batch:  "b2",
		call:   "prog-call",
		tool:   "update_progress",
		digest: "plan-1",
		status: api.InvocationStatusCompleted,
	})
	gotAfterProgress := projectExecutionState([]api.Message{rejectedCmd, progressDone}, "")
	if len(gotAfterProgress.Operations) != 1 || !gotAfterProgress.Operations[0].ChecklistAfter {
		t.Fatalf("expected ChecklistAfter=true after update_progress: %+v", gotAfterProgress.Operations)
	}

	retryCmd := receiptMessage(receiptSpec{
		batch:   "b3",
		call:    "cmd-2",
		tool:    "command",
		digest:  "mkdir-game",
		status:  api.InvocationStatusCompleted,
		verdict: api.SourceVerdictPassed,
		args:    map[string]any{"command": "mkdir -p tic-tac-toe"},
	})
	gotAfterRetry := projectExecutionState([]api.Message{rejectedCmd, progressDone, retryCmd}, "")
	if len(gotAfterRetry.Operations) != 0 || gotAfterRetry.Recovery {
		t.Fatalf("expected identical command retry to resolve rejected command program: %+v", gotAfterRetry)
	}
}
