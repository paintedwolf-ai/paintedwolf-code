package wiring

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type workflowTurn struct {
	callID  string
	command string
	inspect func(t *testing.T, prev command.ProgramOutcome)
}

type workflowScriptLLM struct {
	t       *testing.T
	fixture *commandWorkflowFixture
	turns   []workflowTurn
	mu      sync.Mutex
	step    int
}

func (m *workflowScriptLLM) Complete(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	hasCommand := false
	for _, tool := range req.Tools {
		if tool.Name == "command" {
			hasCommand = true
			break
		}
	}
	if !hasCommand {
		return &modelcall.Completion{Content: "Workflow session."}, nil
	}
	m.mu.Lock()
	idx := m.step
	m.step++
	m.mu.Unlock()
	if idx > 0 && idx <= len(m.turns) && m.turns[idx-1].inspect != nil {
		m.turns[idx-1].inspect(m.t, m.fixture.outcome(m.t, m.turns[idx-1].callID))
	}
	if idx >= len(m.turns) {
		return &modelcall.Completion{Content: "Workflow complete."}, nil
	}
	turn := m.turns[idx]
	return &modelcall.Completion{ToolCalls: []wire.ToolCall{{
		ID:   turn.callID,
		Name: "command",
		Args: map[string]any{"command": turn.command},
	}}}, nil
}

func (m *workflowScriptLLM) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	comp, err := m.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan modelcall.StreamChunk, 1)
	if len(comp.ToolCalls) > 0 {
		ch <- modelcall.StreamChunk{ToolCalls: comp.ToolCalls, Done: true}
	} else {
		ch <- modelcall.StreamChunk{Content: comp.Content, Done: true}
	}
	close(ch)
	return ch, nil
}

type commandWorkflowFixture struct {
	h       *Harness
	dir     string
	session string
}

type programRow struct {
	id, parentReceipt, state, stopCode, sourceDigest string
	status                                           int
}

type execRow struct {
	operation, state, stopCode, certainty, ownerRef string
	jobHandle, conflictPath, expectedRev, observed  string
	nativeGap                                       string
	status                                          int
}

type pinRow struct {
	role, mode, path, sha256 string
}

func newCommandWorkflowFixture(t *testing.T, turns []workflowTurn) *commandWorkflowFixture {
	return newCommandWorkflowFixtureWithApprovalRules(t, turns, "", true)
}

func newCommandWorkflowFixtureWithApprovalRules(t *testing.T, turns []workflowTurn, approvalRules string, bypass bool) *commandWorkflowFixture {
	t.Helper()
	if bypass {
		t.Setenv("LYCAON_BYPASS_APPROVALS", "1")
	} else {
		t.Setenv("LYCAON_BYPASS_APPROVALS", "0")
	}
	f := &commandWorkflowFixture{dir: t.TempDir()}
	llmClient := &workflowScriptLLM{t: t, fixture: f, turns: turns}
	f.h = BuildForTest(t, WithLLMClient(llmClient))
	overlay := filepath.Join(f.dir, settingsoverlay.DirName())
	testutil.FailErr(t, "create overlay", os.MkdirAll(overlay, 0o700))
	rules := "rules:\n  - category: tool\n    pattern: command\n    effect: allow\n  - category: tool\n    pattern: write\n    effect: allow\n" + approvalRules
	testutil.FailErr(t, "write approvals", os.WriteFile(filepath.Join(overlay, "approvals.yaml"), []byte(rules), 0o600))
	sess, err := f.h.CreateHarnessSession(t, wire.CreateSessionRequest{}, f.dir)
	testutil.FailErr(t, "create session", err)
	f.session = sess.ID
	AttachDefaultAmbient(t, f.h, t.Context(), sess.ID)
	testutil.FailErr(t, "orient workflow", f.h.Workflows.Manager.Fanout.RecordBoardOrientReady(t.Context(), sess.ID, "workflow-fixture-orient"))
	testutil.FailErr(t, "set agent type", f.h.Sessions.Manager.Chats.SetAgentType(t.Context(), sess.ID, "implementer"))
	f.h.SeedProgress(t, t.Context(), sess.ID)
	return f
}

func (f *commandWorkflowFixture) seed(t *testing.T, rel, content string) {
	t.Helper()
	full := filepath.Join(f.dir, rel)
	testutil.FailErr(t, "mkdir "+rel, os.MkdirAll(filepath.Dir(full), 0o755))
	testutil.FailErr(t, "write "+rel, os.WriteFile(full, []byte(content), 0o644))
}

func (f *commandWorkflowFixture) readFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, rel))
	testutil.FailErr(t, "read "+rel, err)
	return string(data)
}

func (f *commandWorkflowFixture) run(t *testing.T) {
	t.Helper()
	_, err := f.h.Sessions.Manager.Submissions.Prompt(t.Context(), f.session, "execute workflow")
	testutil.FailErr(t, "prompt workflow", err)
}

func (f *commandWorkflowFixture) outcome(t *testing.T, callID string) command.ProgramOutcome {
	t.Helper()
	msgs, err := f.h.Store.GetMessages(t.Context(), f.session)
	testutil.FailErr(t, "get messages", err)
	for _, raw := range msgs {
		msg := messageview.RedactMessage(raw)
		if msg.Role != wire.MessageRoleTool || msg.ToolResult == nil || msg.ToolResult.ToolCallID != callID {
			continue
		}
		_, _, err := messageview.RetainedContent(raw, "tool_output", callID)
		testutil.FailErr(t, "retained content "+callID, err)
		_, jsonBody, _, ok := hostmarker.SplitToolJSONBody(raw.ToolResult.Content)
		if !ok {
			t.Fatalf("call %s tool result has no json body: %q", callID, raw.ToolResult.Content)
		}
		var out command.ProgramOutcome
		testutil.FailErr(t, "unmarshal outcome "+callID, json.Unmarshal([]byte(jsonBody), &out))
		if out.ProgramID == "" {
			t.Fatalf("call %s missing program_id in %s", callID, jsonBody)
		}
		return out
	}
	t.Fatalf("no tool result found for call %s", callID)
	return command.ProgramOutcome{}
}

func (f *commandWorkflowFixture) programs(t *testing.T) []programRow {
	t.Helper()
	rows, err := f.h.DB.QueryContext(t.Context(), `SELECT p.id, p.parent_receipt_id, p.state, COALESCE(p.status_code, -1), p.stop_code, p.source_digest
		FROM invocation_programs p JOIN invocation_receipts r ON r.id = p.parent_receipt_id
		WHERE r.session_id = ? ORDER BY p.rowid`, f.session)
	testutil.FailErr(t, "query programs", err)
	defer rows.Close()
	var out []programRow
	for rows.Next() {
		var row programRow
		testutil.FailErr(t, "scan program", rows.Scan(&row.id, &row.parentReceipt, &row.state, &row.status, &row.stopCode, &row.sourceDigest))
		out = append(out, row)
	}
	testutil.FailErr(t, "iterate programs", rows.Err())
	return out
}

func (f *commandWorkflowFixture) executions(t *testing.T, programID string) []execRow {
	t.Helper()
	rows, err := f.h.DB.QueryContext(t.Context(), `SELECT operation_id, state, COALESCE(status_code, -1), stop_code, effect_certainty, owner_ref,
		job_handle, conflict_path, conflict_expected_revision, conflict_observed_revision, native_gap_json
		FROM invocation_program_executions WHERE program_id = ? ORDER BY ordinal`, programID)
	testutil.FailErr(t, "query executions", err)
	defer rows.Close()
	var out []execRow
	for rows.Next() {
		var row execRow
		testutil.FailErr(t, "scan execution", rows.Scan(&row.operation, &row.state, &row.status, &row.stopCode, &row.certainty, &row.ownerRef,
			&row.jobHandle, &row.conflictPath, &row.expectedRev, &row.observed, &row.nativeGap))
		out = append(out, row)
	}
	testutil.FailErr(t, "iterate executions", rows.Err())
	return out
}

func (f *commandWorkflowFixture) pins(t *testing.T, programID string) []pinRow {
	t.Helper()
	rows, err := f.h.DB.QueryContext(t.Context(), `SELECT role, mode, path, content_sha256
		FROM invocation_program_revision_pins WHERE program_id = ? ORDER BY execution_id, ordinal`, programID)
	testutil.FailErr(t, "query pins", err)
	defer rows.Close()
	var out []pinRow
	for rows.Next() {
		var row pinRow
		testutil.FailErr(t, "scan pin", rows.Scan(&row.role, &row.mode, &row.path, &row.sha256))
		out = append(out, row)
	}
	testutil.FailErr(t, "iterate pins", rows.Err())
	return out
}

func (f *commandWorkflowFixture) journalCount(t *testing.T, rel string) int {
	t.Helper()
	var count int
	testutil.FailErr(t, "count source_effects", f.h.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM source_effects WHERE path = ?`, rel).Scan(&count))
	return count
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hasExec(rows []execRow, op, state string) bool {
	for _, r := range rows {
		if r.operation == op && r.state == state {
			return true
		}
	}
	return false
}

func hasPin(rows []pinRow, role, mode, suffix, digest string) bool {
	for _, r := range rows {
		if r.role == role && r.mode == mode && strings.HasSuffix(r.path, suffix) && (digest == "" || r.sha256 == digest) {
			return true
		}
	}
	return false
}

func TestWorkflowFindTODOsAndWriteSummary(t *testing.T) {
	const wantSearch = "src/auth.go:2:// TODO: validate token expiry\nsrc/handler.go:3:// TODO: structured error response\n"
	const wantSummary = "# TODO Summary\n" + wantSearch
	f := newCommandWorkflowFixture(t, []workflowTurn{
		{
			callID:  "todo-search",
			command: `grep -rn "TODO:" src | sort`,
			inspect: func(t *testing.T, prev command.ProgramOutcome) {
				if prev.State != "completed" || prev.Status != 0 || prev.Stdout != wantSearch || prev.NativeGaps != 0 {
					t.Fatalf("search turn outcome = %+v", prev)
				}
			},
		},
		{
			callID:  "todo-write",
			command: `printf '# TODO Summary\n%s\n' "$(grep -rn 'TODO:' src | sort)" | tee TODO_SUMMARY.md; wc -l TODO_SUMMARY.md`,
		},
	})
	f.seed(t, "src/auth.go", "package src\n// TODO: validate token expiry\nfunc Auth() {}\n")
	f.seed(t, "src/handler.go", "package src\nfunc Handle() {}\n// TODO: structured error response\n")
	f.seed(t, "src/clean.go", "package src\nfunc Clean() {}\n")
	f.run(t)

	if got := f.readFile(t, "TODO_SUMMARY.md"); got != wantSummary {
		t.Fatalf("TODO_SUMMARY.md = %q, want %q", got, wantSummary)
	}
	progs := f.programs(t)
	if len(progs) != 2 || progs[0].id == progs[1].id || progs[0].state != "completed" || progs[1].state != "completed" {
		t.Fatalf("programs = %+v", progs)
	}
	out2 := f.outcome(t, "todo-write")
	if out2.CompletedEffects != 1 || out2.NativeGaps != 0 || !strings.HasSuffix(out2.Stdout, "3 TODO_SUMMARY.md\n") {
		t.Fatalf("write turn outcome = %+v", out2)
	}
	exec1, exec2 := f.executions(t, progs[0].id), f.executions(t, progs[1].id)
	if !hasExec(exec1, "grep", "completed") || !hasExec(exec1, "sort", "completed") || !hasExec(exec2, "write", "completed") || !hasExec(exec2, "wc", "completed") {
		t.Fatalf("executions turn1=%+v turn2=%+v", exec1, exec2)
	}
	pins1, pins2 := f.pins(t, progs[0].id), f.pins(t, progs[1].id)
	if !hasPin(pins1, "source", "published_disk", "src/auth.go", "") || !hasPin(pins2, "destination", "absent", "TODO_SUMMARY.md", "") || !hasPin(pins2, "source", "published_disk", "TODO_SUMMARY.md", sha256Hex(wantSummary)) {
		t.Fatalf("pins turn1=%+v turn2=%+v", pins1, pins2)
	}
	var stdoutOutputs int
	testutil.FailErr(t, "read stdout outputs", f.h.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_outputs WHERE program_id = ? AND stream = 'stdout' AND complete = 1`, progs[0].id).Scan(&stdoutOutputs))
	if stdoutOutputs < 1 {
		t.Fatalf("stdout outputs = %d, want >= 1", stdoutOutputs)
	}
	if f.journalCount(t, "TODO_SUMMARY.md") != 1 {
		t.Fatalf("journal count = %d, want 1", f.journalCount(t, "TODO_SUMMARY.md"))
	}
}

func TestWorkflowRunTestsFixWithSedInPlaceAndRerun(t *testing.T) {
	const initialCfg = "EXPECTED=41\nACTUAL=42\n"
	const fixedCfg = "EXPECTED=42\nACTUAL=42\n"
	const testProgram = `got=$(grep '^EXPECTED=' calc.cfg | cut -d= -f2); want=$(grep '^ACTUAL=' calc.cfg | cut -d= -f2); if [ "$got" != "$want" ]; then printf 'FAIL: expected %s got %s\n' "$got" "$want"; false; else printf 'PASS: %s == %s\n' "$got" "$want"; fi`
	f := newCommandWorkflowFixture(t, []workflowTurn{
		{
			callID:  "test-initial",
			command: testProgram,
			inspect: func(t *testing.T, prev command.ProgramOutcome) {
				if prev.State != "completed" || prev.Status != 1 || prev.Stdout != "FAIL: expected 41 got 42\n" {
					t.Fatalf("initial test outcome = %+v", prev)
				}
			},
		},
		{
			callID:  "test-fix",
			command: "sed -i 's/EXPECTED=41/EXPECTED=42/' calc.cfg",
			inspect: func(t *testing.T, prev command.ProgramOutcome) {
				if prev.State != "completed" || prev.Status != 0 || prev.NativeGaps != 0 {
					t.Fatalf("sed -i outcome = %+v", prev)
				}
			},
		},
		{
			callID:  "test-rerun",
			command: testProgram,
		},
	})
	f.seed(t, "calc.cfg", initialCfg)
	f.run(t)

	if got := f.readFile(t, "calc.cfg"); got != fixedCfg {
		t.Fatalf("calc.cfg = %q, want %q", got, fixedCfg)
	}
	out3 := f.outcome(t, "test-rerun")
	if out3.State != "completed" || out3.Status != 0 || out3.Stdout != "PASS: 42 == 42\n" {
		t.Fatalf("rerun outcome = %+v", out3)
	}
	progs := f.programs(t)
	if len(progs) != 3 || progs[0].status != 1 || progs[1].status != 0 || progs[2].status != 0 {
		t.Fatalf("programs = %+v", progs)
	}
	exec2 := f.executions(t, progs[1].id)
	if !hasExec(exec2, "sed", "completed") || exec2[0].nativeGap != "" {
		t.Fatalf("sed executions = %+v", exec2)
	}
	pins2 := f.pins(t, progs[1].id)
	if !hasPin(pins2, "destination", "accepted_document", "calc.cfg", sha256Hex(initialCfg)) || !hasPin(pins2, "source", "published_disk", "calc.cfg", sha256Hex(initialCfg)) {
		t.Fatalf("sed pins = %+v", pins2)
	}
	if f.journalCount(t, "calc.cfg") != 1 {
		t.Fatalf("calc.cfg journal count = %d, want 1", f.journalCount(t, "calc.cfg"))
	}
}

func TestWorkflowRenameJSONKeyAcrossFiles(t *testing.T) {
	const apiBefore = "{\"legacy_host\":\"api.internal\",\"port\":8080}\n"
	const apiAfter = "{\"service_host\":\"api.internal\",\"port\":8080}\n"
	const workerBefore = "{\"legacy_host\":\"worker.internal\",\"workers\":4}\n"
	const workerAfter = "{\"service_host\":\"worker.internal\",\"workers\":4}\n"
	const dbUnchanged = "{\"db_name\":\"primary\"}\n"
	f := newCommandWorkflowFixture(t, []workflowTurn{
		{
			callID:  "json-locate",
			command: `grep -l '"legacy_host"' config/*.json | sort`,
			inspect: func(t *testing.T, prev command.ProgramOutcome) {
				if prev.Stdout != "config/api.json\nconfig/worker.json\n" || prev.NativeGaps != 0 {
					t.Fatalf("locate outcome = %+v", prev)
				}
			},
		},
		{
			callID:  "json-rename",
			command: `for file in $(grep -l '"legacy_host"' config/*.json | sort); do sed -i 's/"legacy_host":/"service_host":/g' "$file"; done; grep -H '"service_host"' config/*.json | sort`,
		},
	})
	f.seed(t, "config/api.json", apiBefore)
	f.seed(t, "config/worker.json", workerBefore)
	f.seed(t, "config/db.json", dbUnchanged)
	f.run(t)

	if f.readFile(t, "config/api.json") != apiAfter || f.readFile(t, "config/worker.json") != workerAfter || f.readFile(t, "config/db.json") != dbUnchanged {
		t.Fatalf("unexpected json files after rename")
	}
	out2 := f.outcome(t, "json-rename")
	wantOut := "config/api.json:" + apiAfter + "config/worker.json:" + workerAfter
	if out2.State != "completed" || out2.Status != 0 || out2.NativeGaps != 0 || out2.Stdout != wantOut {
		t.Fatalf("rename outcome = %+v", out2)
	}
	progs := f.programs(t)
	pins2 := f.pins(t, progs[1].id)
	if !hasPin(pins2, "destination", "accepted_document", "config/api.json", sha256Hex(apiBefore)) ||
		!hasPin(pins2, "destination", "accepted_document", "config/worker.json", sha256Hex(workerBefore)) ||
		!hasPin(pins2, "source", "published_disk", "config/api.json", sha256Hex(apiAfter)) ||
		!hasPin(pins2, "source", "published_disk", "config/worker.json", sha256Hex(workerAfter)) {
		t.Fatalf("rename pins = %+v", pins2)
	}
	if f.journalCount(t, "config/api.json") != 1 || f.journalCount(t, "config/worker.json") != 1 || f.journalCount(t, "config/db.json") != 0 {
		t.Fatalf("unexpected journal counts for json rename")
	}
}

func TestWorkflowFailedRedirectPublishThenTargetedCorrection(t *testing.T) {
	const initialTarget = "initial\n"
	const concurrentTarget = "concurrent-edit\n"
	const resolvedTarget = "merged-payload\n"
	f := newCommandWorkflowFixture(t, []workflowTurn{
		{
			callID:  "conflict-turn",
			command: `printf 'step1-complete\n' | tee step1.txt; { printf 'concurrent-edit\n' > target.txt; printf 'staged-payload\n'; } > target.txt; printf 'step3-never\n' | tee step3.txt`,
			inspect: func(t *testing.T, prev command.ProgramOutcome) {
				if prev.State != "rejected" || prev.StopCode != "COMMAND_REVISION_CONFLICT" || prev.CompletedEffects != 2 || prev.Refusal == nil || prev.Refusal.Code != "COMMAND_REVISION_CONFLICT" {
					t.Fatalf("conflict turn outcome = %+v", prev)
				}
			},
		},
		{
			callID:  "recover-turn",
			command: `cat target.txt; printf 'merged-payload\n' | tee target.txt; printf 'step3-done\n' | tee step3.txt`,
		},
	})
	f.seed(t, "target.txt", initialTarget)
	f.run(t)

	if f.readFile(t, "step1.txt") != "step1-complete\n" || f.readFile(t, "target.txt") != resolvedTarget || f.readFile(t, "step3.txt") != "step3-done\n" {
		t.Fatalf("unexpected files after conflict recovery")
	}
	progs := f.programs(t)
	if len(progs) != 2 || progs[0].id == progs[1].id || progs[0].state != "rejected" || progs[0].stopCode != "COMMAND_REVISION_CONFLICT" || progs[1].state != "completed" {
		t.Fatalf("programs = %+v", progs)
	}
	var conflictRow execRow
	for _, row := range f.executions(t, progs[0].id) {
		if row.conflictPath != "" {
			conflictRow = row
		}
	}
	if !strings.HasSuffix(conflictRow.conflictPath, "target.txt") || !strings.HasSuffix(conflictRow.expectedRev, "@1") || !strings.HasSuffix(conflictRow.observed, "@3") || conflictRow.expectedRev == conflictRow.observed {
		t.Fatalf("conflict receipt = %+v", conflictRow)
	}
	pins2 := f.pins(t, progs[1].id)
	if !hasPin(pins2, "source", "published_disk", "target.txt", sha256Hex(concurrentTarget)) || !hasPin(pins2, "destination", "accepted_document", "target.txt", sha256Hex(concurrentTarget)) {
		t.Fatalf("turn2 pins did not chain from observed conflict revision: %+v", pins2)
	}
	if f.journalCount(t, "step1.txt") != 1 || f.journalCount(t, "target.txt") != 2 || f.journalCount(t, "step3.txt") != 1 {
		t.Fatalf("completed effects duplicated or missing: step1=%d target=%d step3=%d", f.journalCount(t, "step1.txt"), f.journalCount(t, "target.txt"), f.journalCount(t, "step3.txt"))
	}
}

func TestWorkflowBackgroundBuildWaitAndStatusCheck(t *testing.T) {
	var observedHandle string
	f := newCommandWorkflowFixture(t, []workflowTurn{
		{
			callID:  "bg-launch-wait",
			command: `mkdir -p dist; { sleep 0.2; printf 'build-log:ok\n' > dist/build.log; printf 'artifact:v1\n' | tee dist/app.bin >/dev/null; } & job=$!; jobs; wait "$job"; printf 'wait_status=%d job=%s\n' "$?" "$job"`,
			inspect: func(t *testing.T, prev command.ProgramOutcome) {
				if prev.State != "completed" || prev.Status != 0 || !strings.Contains(prev.Stdout, " running\n") || !strings.Contains(prev.Stdout, "wait_status=0 job=") {
					t.Fatalf("bg turn outcome = %+v", prev)
				}
				_, observedHandle, _ = strings.Cut(prev.Stdout, "wait_status=0 job=")
				observedHandle = strings.TrimSpace(observedHandle)
			},
		},
		{
			callID:  "bg-check-artifact",
			command: `ls dist/build.log dist/app.bin >/dev/null && cat dist/build.log dist/app.bin`,
		},
	})
	f.run(t)

	if f.readFile(t, "dist/build.log") != "build-log:ok\n" || f.readFile(t, "dist/app.bin") != "artifact:v1\n" {
		t.Fatalf("background artifacts mismatch")
	}
	out2 := f.outcome(t, "bg-check-artifact")
	if out2.State != "completed" || out2.Status != 0 || out2.Stdout != "build-log:ok\nartifact:v1\n" {
		t.Fatalf("artifact check outcome = %+v", out2)
	}
	progs := f.programs(t)
	exec1 := f.executions(t, progs[0].id)
	var handleInReceipt string
	for _, row := range exec1 {
		if row.jobHandle != "" {
			handleInReceipt = row.jobHandle
		}
	}
	if handleInReceipt == "" || handleInReceipt != observedHandle || !hasExec(exec1, "jobs", "completed") || !hasExec(exec1, "wait", "completed") || !hasExec(exec1, "write", "completed") {
		t.Fatalf("bg executions = %+v (handle=%q want %q)", exec1, handleInReceipt, observedHandle)
	}
	pins2 := f.pins(t, progs[1].id)
	if !hasPin(pins2, "source", "published_disk", "dist/build.log", sha256Hex("build-log:ok\n")) || !hasPin(pins2, "source", "published_disk", "dist/app.bin", sha256Hex("artifact:v1\n")) {
		t.Fatalf("bg verify pins = %+v", pins2)
	}
}

func TestWorkflowProgramRefusedMidwayAndModelRecovers(t *testing.T) {
	f := newCommandWorkflowFixture(t, []workflowTurn{
		{
			callID:  "midway-refused",
			command: `printf 'prepared-header\n' | tee header.txt; summarize --unsupported-flag notes.txt; printf 'unreachable\n' | tee tail.txt`,
			inspect: func(t *testing.T, prev command.ProgramOutcome) {
				if prev.State != "rejected" || prev.Status != 2 || prev.StopCode != "COMMAND_OPTION_UNSUPPORTED" ||
					prev.Stdout != "prepared-header\n" || prev.CompletedEffects != 1 || prev.Refusal == nil || prev.Refusal.Code != "COMMAND_OPTION_UNSUPPORTED" {
					t.Fatalf("midway refusal outcome = %+v", prev)
				}
			},
		},
		{
			callID:  "midway-recover",
			command: `head -n 1 notes.txt | tee body.txt; printf 'tail-complete\n' | tee tail.txt; cat header.txt body.txt tail.txt`,
		},
	})
	f.seed(t, "notes.txt", "alpha\nbeta\n")
	f.run(t)

	if f.readFile(t, "header.txt") != "prepared-header\n" || f.readFile(t, "body.txt") != "alpha\n" || f.readFile(t, "tail.txt") != "tail-complete\n" {
		t.Fatalf("unexpected files after midway refusal recovery")
	}
	out2 := f.outcome(t, "midway-recover")
	if out2.State != "completed" || out2.Status != 0 || out2.CompletedEffects != 2 || out2.Stdout != "alpha\ntail-complete\nprepared-header\nalpha\ntail-complete\n" {
		t.Fatalf("recovery outcome = %+v", out2)
	}
	progs := f.programs(t)
	if progs[0].state != "rejected" || progs[0].stopCode != "COMMAND_OPTION_UNSUPPORTED" {
		t.Fatalf("turn1 program = %+v", progs[0])
	}
	exec1 := f.executions(t, progs[0].id)
	if len(exec1) != 3 || !hasExec(exec1, "printf", "completed") || !hasExec(exec1, "write", "completed") {
		t.Fatalf("turn1 executions = %+v", exec1)
	}
	var unstarted int
	testutil.FailErr(t, "count unstarted nodes", f.h.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_nodes WHERE program_id = ? AND state = 'not_started'`, progs[0].id).Scan(&unstarted))
	if unstarted == 0 {
		t.Fatal("expected unstarted nodes after midway refusal")
	}
	if f.journalCount(t, "header.txt") != 1 || f.journalCount(t, "body.txt") != 1 || f.journalCount(t, "tail.txt") != 1 {
		t.Fatalf("completed effects duplicated on recovery: header=%d body=%d tail=%d", f.journalCount(t, "header.txt"), f.journalCount(t, "body.txt"), f.journalCount(t, "tail.txt"))
	}
}
