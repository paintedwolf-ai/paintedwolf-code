package command_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

type programFixture struct{ *programtest.Fixture }

func newProgramFixture(t *testing.T) *programFixture {
	t.Helper()
	return &programFixture{programtest.New(t, programtest.Options{Natives: func(dir string) map[string]command.NativeOperation {
		return map[string]command.NativeOperation{"tee": teeOperation(dir), "head": firstLineOperation()}
	}})}
}

func newLoopFixture(t *testing.T) *programFixture {
	t.Helper()
	return &programFixture{programtest.New(t, programtest.Options{
		Input:       func(dir string) commandprogram.InputRedirection { return loopInput{dir: dir} },
		Publication: func(string) commandprogram.OutputPublication { return nullPublication{} },
	})}
}

type loopInput struct{ dir string }

func (i loopInput) OpenInput(_ context.Context, destination commandprogram.InputDestination) (io.ReadCloser, error) {
	return os.Open(filepath.Join(i.dir, destination.Target.Value))
}

type nullPublication struct{}

func (nullPublication) Open(_ context.Context, destination commandprogram.OutputDestination) (commandprogram.StagedOutput, error) {
	if destination.Target.Value != "/dev/null" {
		return nil, fmt.Errorf("fixture only supports /dev/null output")
	}
	return nullStage{}, nil
}

type nullStage struct{}

func (nullStage) Write(p []byte) (int, error) { return len(p), nil }
func (nullStage) Publish(context.Context) (commandprogram.OutputPublished, error) {
	return commandprogram.OutputPublished{}, nil
}
func (nullStage) Abandon(context.Context) error { return nil }

func (f *programFixture) call(t *testing.T, ctx context.Context, source string) (string, error) {
	t.Helper()
	return f.Call(t, ctx, source)
}

func (f *programFixture) programRows(t *testing.T) []string { return f.Programs(t) }

func (f *programFixture) executions(t *testing.T) map[string]string { return f.Executions(t) }

// firstLineOperation is a typed fixture reader that copies one stdin line and
// then stops reading, as head does.
func firstLineOperation() command.NativeOperation {
	return command.NativeOperation{Tool: "read", Prepare: func(_ context.Context, _ *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
		bound, err := commandprogram.CatalogBinder{}.Bind(catalogSpec("head"), request.Argv)
		if err != nil {
			return command.NativePrepared{}, err
		}
		return command.NativePrepared{Review: bound.Review("read"), Invoke: func(_ context.Context, _ tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
			line, err := bufio.NewReader(streams.Input).ReadString('\n')
			_ = streams.Input.Close()
			if err == nil {
				_, err = io.WriteString(streams.Output, line)
			}
			if err != nil {
				return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
			}
			return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
		}}, nil
	}}
}

// teeOperation is a typed fixture owner bound through InvokeNative: it copies
// its actual stdin to the bound operand and to stdout.
func teeOperation(dir string) command.NativeOperation {
	return command.NativeOperation{Tool: "write", Prepare: func(_ context.Context, _ *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
		bound, err := commandprogram.CatalogBinder{}.Bind(catalogSpec("tee"), request.Argv)
		if err != nil {
			return command.NativePrepared{}, err
		}
		operands := bound.Operands()
		if len(operands) != 1 {
			return command.NativePrepared{}, toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"reason": "one target"})
		}
		target := filepath.Join(dir, operands[0].Value)
		return command.NativePrepared{Mode: "each-target", Review: bound.Review("write"), Invoke: func(_ context.Context, _ tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
			data, err := io.ReadAll(streams.Input)
			if err == nil {
				err = os.WriteFile(target, data, 0o600)
			}
			if err == nil {
				_, err = streams.Output.Write(data)
			}
			if err != nil {
				return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.EffectUnknown}, err
			}
			return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.EffectCompleted}, nil
		}}, nil
	}}
}

func catalogSpec(name string) toolcontract.CommandSpecification {
	catalog, _ := toolcontract.LanguageV1()
	for _, spec := range catalog.Commands {
		if spec.Name == name {
			return spec
		}
	}
	return toolcontract.CommandSpecification{}
}

// awaitExecution waits until an execution of operation reaches state.
func (f *programFixture) awaitExecution(t *testing.T, operation, state string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(f.executions(t)[operation], state) {
		if time.Now().After(deadline) {
			t.Fatalf("%s never reached %s: %v", operation, state, f.executions(t))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// jobHandle reads the single session job handle the jobs builtin lists.
func (f *programFixture) jobHandle(t *testing.T) string {
	t.Helper()
	listed, err := f.call(t, t.Context(), "jobs")
	testutil.FailErr(t, "list session jobs", err)
	var outcome struct{ Stdout string }
	testutil.FailErr(t, "decode jobs outcome", json.Unmarshal([]byte(listed), &outcome))
	fields := strings.Fields(outcome.Stdout)
	if len(fields) != 3 || fields[0] != "[1]" || fields[2] != "running" {
		t.Fatalf("jobs output %q", outcome.Stdout)
	}
	return fields[1]
}

func (f *programFixture) registryHandle(t *testing.T) string {
	t.Helper()
	jobs := f.Background.ActiveJobs(f.Session)
	if len(jobs) != 1 {
		t.Fatalf("expected one active background job, got %+v", jobs)
	}
	return jobs[0].Handle
}

func TestProgramEntryRunsNativeAndExternalStagesWithChildReceipts(t *testing.T) {
	f := newProgramFixture(t)
	out, err := f.call(t, t.Context(), "external -- /bin/echo hello | tee out.txt")
	testutil.FailErr(t, "run native and external program", err)
	if !strings.Contains(out, `"stdout":"hello\n"`) || !strings.Contains(out, `"state":"completed"`) {
		t.Fatalf("program outcome %s", out)
	}
	data, err := os.ReadFile(filepath.Join(f.Dir, "out.txt"))
	if err != nil || string(data) != "hello\n" {
		t.Fatalf("native owner effect %q %v", data, err)
	}
	executions := f.executions(t)
	if executions["command"] != "completed;" || executions["write"] != "completed;" {
		t.Fatalf("child receipts %v", executions)
	}
	if programs := f.programRows(t); len(programs) != 1 || programs[0] != "completed" {
		t.Fatalf("program receipts %v", programs)
	}
}

func TestProgramEntryRefusesExcludedConstructBeforeEarlierWrite(t *testing.T) {
	f := newProgramFixture(t)
	_, err := f.call(t, t.Context(), "external -- /bin/echo x | tee first.txt; trap ls")
	rejected := toolrejection.AsToolReject(err)
	if rejected == nil || !strings.HasPrefix(rejected.Code, "COMMAND_SYNTAX_") || rejected.Data["source_start_byte"] == nil || rejected.Data["source_line"] != 1 {
		t.Fatalf("excluded construct %v", err)
	}
	if start, _ := rejected.Data["source_start_byte"].(int); start < len("external -- /bin/echo x | tee first.txt; ") {
		t.Fatalf("span does not locate the excluded construct: %v", rejected.Data)
	}
	if _, statErr := os.Stat(filepath.Join(f.Dir, "first.txt")); !os.IsNotExist(statErr) {
		t.Fatal("earlier valid write ran before whole-program validation")
	}
	if programs := f.programRows(t); len(programs) != 0 {
		t.Fatalf("refused program recorded %v", programs)
	}
}

func TestProgramEntryBackgroundJobIsListedAndKilled(t *testing.T) {
	f := newProgramFixture(t)
	_, err := f.call(t, t.Context(), "external -- /bin/sleep 30 &")
	testutil.FailErr(t, "launch background job", err)
	handle := f.jobHandle(t)
	registryHandle := f.registryHandle(t)
	_, err = f.call(t, t.Context(), "kill -TERM "+handle)
	testutil.FailErr(t, "kill session job", err)
	jobs := &command.SessionJobs{Registry: f.Background, SessionID: f.Session}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	exit, stopped, err := f.Background.Lifecycle.Wait(ctx, f.Session, registryHandle)
	if err != nil || !stopped || exit == 0 {
		t.Fatalf("killed job exit=%d stopped=%v err=%v", exit, stopped, err)
	}
	if live, _ := jobs.List(t.Context()); len(live) != 0 {
		t.Fatalf("killed job still listed %v", live)
	}
	for _, reference := range []string{"not-a-session-job", "%2", "9999999"} {
		out, err := f.call(t, t.Context(), "kill -TERM "+reference)
		testutil.FailErr(t, "run foreign kill", err)
		if outcome := decodeOutcome(t, out); outcome.Refusal == nil || outcome.Refusal.Code != "COMMAND_JOB_UNKNOWN" || strings.Contains(outcome.Refusal.Feedback, "process_list") {
			t.Fatalf("foreign job reference %s: %s", reference, out)
		}
	}
}

// %N, %% and $! name this session's jobs for kill and wait.
func TestProgramEntryJobSpecsAddressSessionJobs(t *testing.T) {
	f := newProgramFixture(t)
	out, err := f.call(t, t.Context(), `sleep 30 & first=$!; sleep 30 & kill %1; kill %%; wait $first; external -- /bin/echo "first=$?"; wait %2; external -- /bin/echo "second=$?"`)
	testutil.FailErr(t, "run job specs", err)
	if outcome := decodeOutcome(t, out); !strings.Contains(outcome.Stdout, "first=143") || !strings.Contains(outcome.Stdout, "second=143") {
		t.Fatalf("job spec outcome %s", out)
	}
}

func TestProgramEntryStopCancelsMidPipelineAndSettlesChildren(t *testing.T) {
	f := newProgramFixture(t)
	done := make(chan error, 1)
	go func() {
		_, err := f.call(t, t.Context(), "external -- /bin/sleep 30 | tee never.txt")
		done <- err
	}()
	f.awaitExecution(t, "command", "running")
	if stopped := f.Runtime.Supervisor.Stop(f.Session); len(stopped) != 1 {
		t.Fatalf("stop acknowledged %v", stopped)
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("stop did not settle the program")
	}
	if programs := f.programRows(t); len(programs) != 1 || programs[0] != "interrupted" {
		t.Fatalf("program state %v", programs)
	}
	for operation, states := range f.executions(t) {
		if strings.Contains(states, "running") || strings.Contains(states, "prepared") {
			t.Fatalf("child %s left unsettled: %s", operation, states)
		}
	}
}

func TestProgramSupervisorLossInterruptsWithoutReplay(t *testing.T) {
	f := newProgramFixture(t)
	done := make(chan error, 1)
	go func() {
		_, err := f.call(t, t.Context(), "external -- /bin/sleep 30")
		done <- err
	}()
	f.awaitExecution(t, "command", "running")
	interrupted, err := f.Runtime.Supervisor.Lost(t.Context(), f.Session, "output:supervisor-lost")
	testutil.FailErr(t, "reconcile supervisor loss", err)
	if interrupted != 1 {
		t.Fatalf("loss interrupted %d programs", interrupted)
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("loss did not end the program")
	}
	if programs := f.programRows(t); len(programs) != 1 || programs[0] != "interrupted" {
		t.Fatalf("program state after loss %v", programs)
	}
	before := f.executions(t)
	count, err := f.Runtime.Supervisor.Lost(t.Context(), f.Session, "output:supervisor-lost-again")
	testutil.FailErr(t, "repeat reconciliation", err)
	if count != 0 || fmt.Sprint(f.executions(t)) != fmt.Sprint(before) || before["command"] != "interrupted;" {
		t.Fatalf("loss replayed or rewrote children: before %v after %v count %d", before, f.executions(t), count)
	}
}

func TestProgramEntryDisableStopsOwnedWorkAndRefusesNewPrograms(t *testing.T) {
	f := newProgramFixture(t)
	_, err := f.call(t, t.Context(), "external -- /bin/sleep 30 &")
	testutil.FailErr(t, "launch owned job", err)
	_ = f.jobHandle(t)
	registryHandle := f.registryHandle(t)
	if stopped := f.Runtime.SetDisabled(true); len(stopped) != 1 {
		t.Fatalf("disable stopped %v", stopped)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, stopped, err := f.Background.Lifecycle.Wait(ctx, f.Session, registryHandle); err != nil || !stopped {
		t.Fatalf("owned job after disable stopped=%v err=%v", stopped, err)
	}
	_, err = f.call(t, t.Context(), "external -- /bin/echo after")
	if rejected := toolrejection.AsToolReject(err); rejected == nil || rejected.Code != "COMMAND_PROGRAMS_DISABLED" {
		t.Fatalf("disabled entry admitted a program: %v", err)
	}
	if programs := f.programRows(t); len(programs) != 2 || programs[0] != "interrupted" || programs[1] != "completed" {
		t.Fatalf("programs after disable %v", programs)
	}
	f.Runtime.SetDisabled(false)
	if out, err := f.call(t, t.Context(), "external -- /bin/echo again"); err != nil || !strings.Contains(out, `"stdout":"again\n"`) {
		t.Fatalf("re-enabled entry %s %v", out, err)
	}
}

func TestProgramEntryClassifiesExternalProducerStoppedByConsumer(t *testing.T) {
	f := newProgramFixture(t)
	out, err := f.call(t, t.Context(), "external -- /usr/bin/yes | head")
	testutil.FailErr(t, "run early-consumer pipeline", err)
	if !strings.Contains(out, `"stdout":"y\n"`) || !strings.Contains(out, `"status":0`) || !strings.Contains(out, `"state":"completed"`) {
		t.Fatalf("early consumer outcome %s", out)
	}
	if executions := f.executions(t); executions["command"] != "completed;" || executions["read"] != "completed;" {
		t.Fatalf("children %v", executions)
	}
}

// Desktop screen capture programs are refused at their launch point, after
// earlier statements ran, without starting the capture program.
func TestProgramEntryRefusesDesktopCaptureLaunch(t *testing.T) {
	for _, program := range []string{"screencapture -x out.png", "/usr/sbin/screencapture -x out.png", "scrot shot.png", "xwd -root"} {
		f := newProgramFixture(t)
		out, err := f.call(t, t.Context(), program)
		testutil.FailErr(t, "run refused capture", err)
		if outcome := decodeOutcome(t, out); outcome.Refusal == nil || outcome.Refusal.Code != "DESKTOP_CAPTURE_DISALLOWED" {
			t.Fatalf("%q: expected DESKTOP_CAPTURE_DISALLOWED, got %s", program, out)
		}
		if executions := f.executions(t); executions["command"] != "refused;" {
			t.Fatalf("%q: child receipts %v", program, executions)
		}
	}
}

// Retained program output holds its stored bytes, not the encoder's worst
// case, so one session runs many programs without exhausting its quota.
func TestProgramEntryRunsManyProgramsWithinTheSessionSpillQuota(t *testing.T) {
	f := newProgramFixture(t)
	for i := range 150 {
		out, err := f.call(t, t.Context(), fmt.Sprintf("external -- /bin/echo run-%d | tee out.txt", i))
		if err != nil || !strings.Contains(out, `"state":"completed"`) {
			t.Fatalf("program %d: %v %s", i, err, out)
		}
	}
}

func TestProgramEntryLargeLoopsKeepTheSessionUsable(t *testing.T) {
	f := newLoopFixture(t)
	const items = 12000
	data := strings.Repeat("item\n", items)
	testutil.FailErr(t, "write loop input", os.WriteFile(filepath.Join(f.Dir, "items.txt"), []byte(data), 0o600))
	for _, form := range []struct {
		program       string
		children      int
		receiptBodies int
	}{
		{`while read -r i; do basename "$i" >/dev/null; done < items.txt`, 2*items + 1, 2*items + 2},
		{`for i in $(external -- /bin/cat items.txt); do basename "$i" >/dev/null; done`, items + 1, items + 2},
	} {
		out, err := f.call(t, t.Context(), form.program)
		testutil.FailErr(t, "run large loop", err)
		result := decodeOutcome(t, out)
		if result.State != "completed" || result.Status != 0 || result.Refusal != nil || result.Stdout != "" || result.ExecutedChildren != form.children {
			var events, bytes int64
			testutil.FailErr(t, "read trace usage", f.Database.QueryRowContext(t.Context(), `SELECT trace_events, trace_bytes FROM invocation_programs WHERE id=?`, result.ProgramID).Scan(&events, &bytes))
			t.Fatalf("large loop state=%s status=%d refusal=%+v output=%d children=%d trace=%d/%d", result.State, result.Status, result.Refusal, len(result.Stdout), result.ExecutedChildren, events, bytes)
		}
		var receipts int
		testutil.FailErr(t, "count loop child receipts", f.Database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_executions WHERE program_id=?`, result.ProgramID).Scan(&receipts))
		if receipts != form.receiptBodies {
			t.Fatalf("loop child receipts=%d, want %d for %q", receipts, form.receiptBodies, form.program)
		}
		var inline, spilled int
		testutil.FailErr(t, "count durable loop input bodies", f.Database.QueryRowContext(t.Context(), `SELECT count(*) FILTER (WHERE inline_body IS NOT NULL),count(*) FILTER (WHERE inline_body IS NULL) FROM invocation_program_outputs WHERE program_id=? AND stream='arguments'`, result.ProgramID).Scan(&inline, &spilled))
		if inline < receipts || spilled != 0 {
			t.Fatalf("loop input retention: %d inline, %d blob-backed for %d children", inline, spilled, receipts)
		}
	}
	out, err := f.call(t, t.Context(), "echo hi")
	testutil.FailErr(t, "run later program", err)
	if result := decodeOutcome(t, out); result.State != "completed" || result.Stdout != "hi\n" {
		t.Fatalf("later program failed: %+v", result)
	}
}

func TestProgramEntryChildLimitKeepsPartialResultsAndSessionUsable(t *testing.T) {
	f := newProgramFixture(t)
	seeded := 0
	f.Runtime.Evaluate = func(ctx context.Context, plan commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
		if seeded < 2 {
			if err := seedSettledChildrenAtLimit(ctx, f, plan, ports); err != nil {
				return commandprogram.ProgramOutcome{}, err
			}
			seeded++
		}
		return commandprogram.Evaluate(ctx, plan, ports)
	}
	for _, form := range []struct {
		program, state, stdout, refusal string
	}{
		{":; echo before", "completed", "before\n", ""},
		{":; echo before; echo after", "rejected", "before\n", "COMMAND_LIMIT_EXCEEDED"},
		{"echo hi", "completed", "hi\n", ""},
	} {
		out, err := f.call(t, t.Context(), form.program)
		testutil.FailErr(t, "run child-limit program", err)
		result := decodeOutcome(t, out)
		if result.State != form.state || result.Stdout != form.stdout {
			t.Fatalf("%q: state=%q stdout=%q refusal=%+v", form.program, result.State, result.Stdout, result.Refusal)
		}
		var count int64
		testutil.FailErr(t, "count durable child receipts", f.Database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_executions WHERE program_id=?`, result.ProgramID).Scan(&count))
		expectedCount := int64(65536)
		if form.program == "echo hi" {
			expectedCount = 1
		}
		if count != expectedCount {
			t.Fatalf("%q: child receipts=%d", form.program, count)
		}
		if form.refusal == "" {
			if result.Refusal != nil || result.Status != 0 {
				t.Fatalf("%q: unexpected refusal %+v status=%d", form.program, result.Refusal, result.Status)
			}
			continue
		}
		if result.Refusal == nil || result.Refusal.Code != form.refusal || !strings.Contains(result.Stderr, "child_executions limit") || !strings.Contains(result.Stderr, "[COMMAND_LIMIT_EXCEEDED]") {
			t.Fatalf("%q: untyped or silent child refusal: %+v", form.program, result)
		}
	}
}

// seedSettledChildrenAtLimit composes a valid, contiguous receipt history for
// this running program. The final children still enter through the command
// evaluator and recorder, which decide the exact at/one-past boundary.
func seedSettledChildrenAtLimit(ctx context.Context, f *programFixture, plan commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) error {
	const historical = 65535
	program := ports.Invocation.Identity.ProgramID
	var beforeEvents, beforeBytes int64
	if err := f.Database.QueryRowContext(ctx, `SELECT trace_events,trace_bytes FROM invocation_programs WHERE id=?`, program).Scan(&beforeEvents, &beforeBytes); err != nil {
		return err
	}
	loc := plan.Statements[0].SyntaxLocation
	seed := commandprogram.NextExecution(ports.Invocation, string(loc.ID), loc.Span)
	if err := ports.Executions.BeginExecution(ctx, seed, ":"); err != nil {
		return err
	}
	if err := ports.Executions.SettleExecution(ctx, seed, commandprogram.ChildRecord{Identity: seed.Identity, Command: ":", Executed: true, Result: toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}}); err != nil {
		return err
	}
	var afterEvents, afterBytes int64
	if err := f.Database.QueryRowContext(ctx, `SELECT trace_events,trace_bytes FROM invocation_programs WHERE id=?`, program).Scan(&afterEvents, &afterBytes); err != nil {
		return err
	}
	var definitionBytes int64
	if err := f.Database.QueryRowContext(ctx, `SELECT length(definition_json) FROM invocation_program_definitions WHERE program_id=?`, program).Scan(&definitionBytes); err != nil {
		return err
	}
	_, err := f.Database.ExecContext(ctx, `WITH RECURSIVE seq(n) AS (VALUES(2) UNION ALL SELECT n+1 FROM seq WHERE n<?)
		INSERT INTO invocation_program_executions (id,program_id,node_id,ordinal,parent_execution_id,actor_id,scope_json,operation_id,definition_digest,arguments_representation,arguments_digest,arguments_ref,canonical,state,owner,lifecycle,started_at,settled_at,status_code,complete,publication_eligible,effect_certainty)
		SELECT printf('%s-%s-%s-%s-%012d',substr(replace(e.program_id,'-',''),1,8),substr(replace(e.program_id,'-',''),9,4),substr(replace(e.program_id,'-',''),13,4),substr(replace(e.program_id,'-',''),17,4),n),e.program_id,e.node_id,n,e.parent_execution_id,e.actor_id,e.scope_json,e.operation_id,e.definition_digest,e.arguments_representation,e.arguments_digest,e.arguments_ref,e.canonical,'completed',e.owner,e.lifecycle,e.started_at,e.settled_at,0,1,1,'none'
		FROM seq CROSS JOIN invocation_program_executions e WHERE e.id=?`, historical, seed.Identity.ExecutionID)
	if err != nil {
		return err
	}
	_, err = f.Database.ExecContext(ctx, `UPDATE invocation_programs SET trace_events=trace_events+?,trace_bytes=trace_bytes+? WHERE id=?`, (afterEvents-beforeEvents-1)*(historical-1), (afterBytes-beforeBytes-definitionBytes)*(historical-1), program)
	return err
}

// A native name without a native owner runs as its installed program, marked
// external in its receipt with a native_gap fact; without one it refuses in
// shell voice naming the command.
func TestProgramEntryLaunchesNativeGapsAndRecordsThem(t *testing.T) {
	f := newProgramFixture(t)
	out, err := f.call(t, t.Context(), "external -- /bin/echo gap-line | grep gap")
	testutil.FailErr(t, "run native gap", err)
	if !strings.Contains(out, `"stdout":"gap-line\n"`) || decodeOutcome(t, out).NativeGaps != 1 {
		t.Fatalf("gap launch outcome %s", out)
	}
	var operation, gap string
	testutil.FailErr(t, "read gap receipt", f.Database.QueryRowContext(t.Context(), `SELECT operation_id, native_gap_json FROM invocation_program_executions WHERE native_gap_json != ''`).Scan(&operation, &gap))
	if operation != "command" || !strings.Contains(gap, `"command":"grep"`) || !strings.Contains(gap, `"code":"COMMAND_NATIVE_FORM_UNSUPPORTED"`) {
		t.Fatalf("gap receipt %s %s", operation, gap)
	}
	out, err = f.call(t, t.Context(), "external -- /bin/echo before; repo-map; external -- /bin/echo never")
	testutil.FailErr(t, "run refused native name", err)
	outcome := decodeOutcome(t, out)
	if outcome.Refusal == nil || outcome.Refusal.Code != "COMMAND_NATIVE_FORM_UNSUPPORTED" || !strings.Contains(outcome.Refusal.Feedback, "COMMAND_NATIVE_FORM_UNSUPPORTED") {
		t.Fatalf("uninstalled native name: %s", out)
	}
	if outcome.Refusal.Details["command"] != "repo-map" || outcome.Refusal.Details["source_line"] != float64(1) {
		t.Fatalf("refusal lost its command or source: %+v", outcome.Refusal)
	}
	if outcome.Stdout != "before\n" || outcome.ProgramID == "" || !strings.Contains(outcome.Stderr, "repo-map: request: ") || !strings.Contains(outcome.Stderr, "[COMMAND_NATIVE_FORM_UNSUPPORTED]") {
		t.Fatalf("refusal replaced the partial result: %+v", outcome)
	}
}

func decodeOutcome(t *testing.T, out string) command.ProgramOutcome {
	t.Helper()
	var outcome command.ProgramOutcome
	testutil.FailErr(t, "decode program outcome", json.Unmarshal([]byte(out), &outcome))
	return outcome
}

// File predicates read the program's files from the statement's directory.
func TestProgramEntryFilePredicatesReadRealFilesFromTheFrame(t *testing.T) {
	f := newProgramFixture(t)
	testutil.FailErr(t, "seed dir", os.MkdirAll(filepath.Join(f.Dir, "sub"), 0o755))
	testutil.FailErr(t, "seed file", os.WriteFile(filepath.Join(f.Dir, "a.txt"), []byte("a"), 0o644))
	testutil.FailErr(t, "seed nested file", os.WriteFile(filepath.Join(f.Dir, "sub", "b.txt"), []byte("b"), 0o644))
	out, err := f.call(t, t.Context(), `test -f a.txt && [ -d sub ] && [[ -f sub/b.txt ]] && ! test -f missing && ! [ -d a.txt ] && cd sub && test -f b.txt; external -- /bin/echo "status=$?"`)
	testutil.FailErr(t, "run predicates", err)
	if outcome := decodeOutcome(t, out); outcome.Stdout != "status=0\n" {
		t.Fatalf("predicates outcome %s", out)
	}
}

// With the external selector hidden, `external --` refuses with a typed code
// while a native gap still launches the installed program and is counted.
func TestProgramEntryHiddenExternalSelectorRefusesButGapsRun(t *testing.T) {
	f := newProgramFixture(t)
	f.Runtime.ExternalHidden = true
	out, err := f.call(t, t.Context(), "grep -q nothing /dev/null; external -- /bin/echo never")
	testutil.FailErr(t, "run hidden selector", err)
	outcome := decodeOutcome(t, out)
	if outcome.Refusal == nil || outcome.Refusal.Code != "COMMAND_EXTERNAL_HIDDEN" || outcome.Stdout != "" || outcome.NativeGaps != 1 {
		t.Fatalf("hidden external outcome %s", out)
	}
	if !strings.Contains(outcome.Stderr, "external: request: ") || !strings.Contains(outcome.Stderr, "[COMMAND_EXTERNAL_HIDDEN]") {
		t.Fatalf("hidden external stderr %q", outcome.Stderr)
	}
}

// A settled program states its run for verification evidence: passed on
// status 0, failed otherwise; a program whose job still runs states nothing.
func TestProgramEntryStatesTheSettledRun(t *testing.T) {
	for _, c := range []struct {
		program, verdict string
	}{{"true", api.SourceVerdictPassed}, {"false", api.SourceVerdictFailed}, {"sleep 5 &", ""}} {
		f := newProgramFixture(t)
		out := &tools.ToolInvocationOut{}
		f.Context = func(_ string, tc *tools.ToolContext) { tc.Effects.Out = out }
		_, err := f.call(t, t.Context(), c.program)
		testutil.FailErr(t, "run "+c.program, err)
		switch {
		case c.verdict == "" && out.SourceRun != nil:
			t.Fatalf("%s with a running job stated %+v", c.program, out.SourceRun)
		case c.verdict != "" && (out.SourceRun == nil || out.SourceRun.Verdict != c.verdict || out.SourceRun.Command != c.program):
			t.Fatalf("%s stated %+v, want %s", c.program, out.SourceRun, c.verdict)
		}
	}
}
