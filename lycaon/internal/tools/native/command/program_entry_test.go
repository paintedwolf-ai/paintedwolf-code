package command_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/pkg/api"
)

type programFixture struct {
	runtime  *command.ProgramRuntime
	recorder *invocation.SQLRecorder
	database db.Handle
	session  string
	dir      string
	calls    int
}

type fixtureOwner struct{}

func (fixtureOwner) HostOwner(context.Context) (people.Person, error) {
	return people.Person{ID: "fixture-person", Role: api.PersonRoleOwner}, nil
}

type progressFacts struct{ store progress.Store }

func (p progressFacts) ProgramProgressState(ctx context.Context, id string) (progress.PrerequisiteState, error) {
	return progress.PrerequisiteState{SessionID: id, RootSessionID: id, Content: p.store.Get(ctx, id)}, nil
}

// newProgramFixture composes the real registry, executor, SQL receipts,
// session process registry and supervisor around the command entry.
func newProgramFixture(t *testing.T, evaluate commandprogram.Evaluator) *programFixture {
	t.Helper()
	dir := t.TempDir()
	database := testdbfixture.Open(t, "programs.db")
	session := "session-1"
	testdbseed.InsertSession(t, database, session, testdbseed.DefaultProjectID)
	recorder := invocation.NewSQLRecorder(database)
	registry := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register write owner", registry.Register("write", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return "", errors.New("structured write is not exercised")
	}))
	classes, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation)
	testutil.FailErr(t, "compile profile effects", err)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "fixture", CommandEffects: classes}})
	executor := toolexecution.NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), registry, "fixture")
	owner := progress.NewMemoryStore()
	testutil.FailErr(t, "set actual progress", owner.Set(session, "- [ ] Run the fixture program"))
	background := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	t.Cleanup(func() { _ = background.Lifecycle.Close(context.Background()) })
	runtime := &command.ProgramRuntime{
		Executor: executor, Registry: registry, Recorder: recorder, Blobs: blobstore.Store{Root: t.TempDir()},
		Projector: captureprojection.New(secretmatch.NewInertMatcher(), nil), Background: background,
		Progress: progressFacts{owner}, Supervisor: &command.ProgramSupervisor{Interrupter: recorder}, Evaluate: evaluate,
		Owners:  fixtureOwner{},
		Natives: map[string]command.NativeOperation{"tee": teeOperation(dir)},
	}
	return &programFixture{runtime: runtime, recorder: recorder, database: database, session: session, dir: dir}
}

// call opens a real model receipt for one command call and runs the entry.
func (f *programFixture) call(t *testing.T, ctx context.Context, source string) (string, error) {
	t.Helper()
	f.calls++
	callID := fmt.Sprintf("model-call-%d", f.calls)
	args := map[string]any{"command": source}
	contract, _ := toolcontract.Lookup("command")
	parent, err := f.recorder.Begin(t.Context(), invocation.Start{ProjectID: testdbseed.DefaultProjectID, SessionID: f.session, ToolCallID: callID, ToolName: "command", Args: args, Contract: contract})
	testutil.FailErr(t, "begin model receipt", err)
	tc := nativefixture.Context(f.dir)
	tc.Identity.Agent, tc.Identity.ProjectID, tc.Identity.SessionID, tc.Identity.ToolCallID = "fixture", testdbseed.DefaultProjectID, f.session, callID
	tc.Invocation.ID = parent.ID
	return f.runtime.Run(ctx, args, tc)
}

func (f *programFixture) programRows(t *testing.T) []string {
	t.Helper()
	rows, err := f.database.QueryContext(t.Context(), `SELECT p.state FROM invocation_programs p ORDER BY p.started_at`)
	testutil.FailErr(t, "read programs", err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var state string
		testutil.FailErr(t, "scan program", rows.Scan(&state))
		out = append(out, state)
	}
	return out
}

func (f *programFixture) executions(t *testing.T) map[string]string {
	t.Helper()
	rows, err := f.database.QueryContext(t.Context(), `SELECT operation_id, state FROM invocation_program_executions ORDER BY ordinal`)
	testutil.FailErr(t, "read executions", err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var operation, state string
		testutil.FailErr(t, "scan execution", rows.Scan(&operation, &state))
		out[operation] += state + ";"
	}
	return out
}

// teeOperation is a typed fixture owner bound through InvokeNative: it copies
// its actual stdin to the reviewed operand and to stdout.
func teeOperation(dir string) command.NativeOperation {
	return command.NativeOperation{Tool: "write", Mode: func(commandprogram.BoundArgv) string { return "each-target" },
		Bind: func(_ context.Context, _ commandprogram.OperationRequest, _ commandprogram.BoundArgv, bound toolcontract.BoundOperation) (toolexecution.NativeEffect, error) {
			return toolexecution.NativeEffect{PrepareReviewed: func(_ context.Context, resolved map[string]any, _ tools.ToolContext) (toolexecution.PreparedNativeEffect, error) {
				operands, _ := resolved["operands"].([]any)
				if len(operands) != 1 {
					return toolexecution.PreparedNativeEffect{}, toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"reason": "one target"})
				}
				target := filepath.Join(dir, operands[0].(string))
				return toolexecution.PreparedNativeEffect{Operation: bound, InvokeOwner: func(_ context.Context, _ tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
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
			}}, nil
		}}
}

// literalEvaluator stands in for the statement scheduler: literal words,
// sequences, two-stage pipelines and trailing & only.
type literalEvaluator struct {
	mu      sync.Mutex
	started chan struct{}
	handles []string
}

func (e *literalEvaluator) evaluate(ctx context.Context, ev commandprogram.Evaluation) (toolcontract.ProducerResult, error) {
	last := toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}
	for _, statement := range ev.Plan.Statements {
		result, err := e.statement(ctx, ev, statement, ev.Streams)
		if err != nil {
			return result, err
		}
		last = result
	}
	return last, nil
}

func (e *literalEvaluator) statement(ctx context.Context, ev commandprogram.Evaluation, statement commandsurface.Statement, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	if statement.Background {
		handle, err := ev.Ports.Jobs.Start(ctx, commandprogram.JobLaunch{Invocation: ev.Root, CommandLine: "background", Run: func(ctx context.Context, job commandprogram.Streams) (toolcontract.ProducerResult, error) {
			foreground := statement
			foreground.Background = false
			return e.statement(ctx, ev, foreground, job)
		}})
		if err != nil {
			return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
		}
		e.mu.Lock()
		e.handles = append(e.handles, handle)
		e.mu.Unlock()
		return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}, nil
	}
	switch node := statement.Command.(type) {
	case commandsurface.SimpleCommand:
		var argv commandprogram.Fields
		for _, word := range node.Words {
			argv = append(argv, commandprogram.Field{Value: literal(word), Span: word.Span})
		}
		request := commandprogram.OperationRequest{Invocation: commandprogram.NextExecution(ev.Root, string(statement.ID), statement.Span), OperationID: argv[0].Value, Statement: statement, Argv: argv}
		if e.started != nil && argv[0].Value == "external" {
			close(e.started)
			e.started = nil
		}
		return ev.Ports.Dispatch.Invoke(ctx, request, streams)
	case commandsurface.BinaryCommand:
		if node.Operator != "|" {
			return toolcontract.ProducerResult{}, fmt.Errorf("fixture evaluator supports pipelines only")
		}
		budget, _ := commandprogram.NewByteBudget("stream_bytes", 1<<20)
		pipe, _ := commandprogram.NewBytePipe(ctx, budget)
		left := make(chan error, 1)
		go func() {
			_, err := e.statement(ctx, ev, node.Left, commandprogram.Streams{Input: commandprogram.NullInput(), Output: pipe, Error: streams.Error})
			_ = pipe.CloseProducer(err)
			left <- err
		}()
		result, err := e.statement(ctx, ev, node.Right, commandprogram.Streams{Input: pipe.ConsumerEnd(), Output: streams.Output, Error: streams.Error})
		return result, errors.Join(err, <-left)
	}
	return toolcontract.ProducerResult{}, fmt.Errorf("fixture evaluator does not support %T", statement.Command)
}

func literal(word commandsurface.DeferredWord) string {
	var out strings.Builder
	for _, part := range word.Parts {
		switch part := part.(type) {
		case commandsurface.LiteralPart:
			out.WriteString(part.Value)
		case commandsurface.SingleQuotedPart:
			out.WriteString(part.Value)
		}
	}
	return out.String()
}

func TestProgramEntryRunsNativeAndExternalStagesWithChildReceipts(t *testing.T) {
	evaluator := &literalEvaluator{}
	f := newProgramFixture(t, evaluator.evaluate)
	out, err := f.call(t, t.Context(), "external -- /bin/echo hello | tee out.txt")
	testutil.FailErr(t, "run native and external program", err)
	if !strings.Contains(out, `"stdout":"hello\n"`) || !strings.Contains(out, `"state":"completed"`) {
		t.Fatalf("program outcome %s", out)
	}
	data, err := os.ReadFile(filepath.Join(f.dir, "out.txt"))
	if err != nil || string(data) != "hello\n" {
		t.Fatalf("native owner effect %q %v", data, err)
	}
	executions := f.executions(t)
	if executions["external"] != "completed;" || executions["write"] != "completed;" {
		t.Fatalf("child receipts %v", executions)
	}
	if programs := f.programRows(t); len(programs) != 1 || programs[0] != "completed" {
		t.Fatalf("program receipts %v", programs)
	}
}

func TestProgramEntryRefusesExcludedConstructBeforeEarlierWrite(t *testing.T) {
	evaluator := &literalEvaluator{}
	f := newProgramFixture(t, evaluator.evaluate)
	_, err := f.call(t, t.Context(), "external -- /bin/echo x | tee first.txt; cat <(ls)")
	rejected := toolrejection.AsToolReject(err)
	if rejected == nil || !strings.HasPrefix(rejected.Code, "COMMAND_SYNTAX_") || rejected.Data["source_start_byte"] == nil || rejected.Data["source_line"] != 1 {
		t.Fatalf("excluded construct %v", err)
	}
	if start, _ := rejected.Data["source_start_byte"].(int); start < len("external -- /bin/echo x | tee first.txt; ") {
		t.Fatalf("span does not locate the excluded construct: %v", rejected.Data)
	}
	if _, statErr := os.Stat(filepath.Join(f.dir, "first.txt")); !os.IsNotExist(statErr) {
		t.Fatal("earlier valid write ran before whole-program validation")
	}
	if programs := f.programRows(t); len(programs) != 0 {
		t.Fatalf("refused program recorded %v", programs)
	}
}

func TestProgramEntryBackgroundJobIsListedAndKilled(t *testing.T) {
	evaluator := &literalEvaluator{}
	f := newProgramFixture(t, evaluator.evaluate)
	_, err := f.call(t, t.Context(), "external -- /bin/sleep 30 &")
	testutil.FailErr(t, "launch background job", err)
	if len(evaluator.handles) != 1 {
		t.Fatalf("job handles %v", evaluator.handles)
	}
	handle := evaluator.handles[0]
	listed, err := f.call(t, t.Context(), "jobs")
	testutil.FailErr(t, "list session jobs", err)
	if !strings.Contains(listed, handle+" running -") {
		t.Fatalf("jobs output %s", listed)
	}
	_, err = f.call(t, t.Context(), "kill -TERM "+handle)
	testutil.FailErr(t, "kill session job", err)
	jobs := &command.SessionJobs{Registry: f.runtime.Background, SessionID: f.session}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	exit, stopped, err := f.runtime.Background.Lifecycle.Wait(ctx, f.session, handle)
	if err != nil || !stopped || exit == 0 {
		t.Fatalf("killed job exit=%d stopped=%v err=%v", exit, stopped, err)
	}
	if live, _ := jobs.List(t.Context()); len(live) != 0 {
		t.Fatalf("killed job still listed %v", live)
	}
	if _, err := f.call(t, t.Context(), "kill -TERM not-a-session-job"); toolrejection.AsToolReject(err) == nil {
		t.Fatalf("foreign job reference accepted: %v", err)
	}
}

func TestProgramEntryStopCancelsMidPipelineAndSettlesChildren(t *testing.T) {
	started := make(chan struct{})
	evaluator := &literalEvaluator{started: started}
	f := newProgramFixture(t, evaluator.evaluate)
	done := make(chan error, 1)
	go func() {
		_, err := f.call(t, t.Context(), "external -- /bin/sleep 30 | tee never.txt")
		done <- err
	}()
	<-started
	deadline := time.Now().Add(10 * time.Second)
	for len(f.runtime.Supervisor.Stop(f.session)) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
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
	started := make(chan struct{})
	evaluator := &literalEvaluator{started: started}
	f := newProgramFixture(t, evaluator.evaluate)
	done := make(chan error, 1)
	go func() {
		_, err := f.call(t, t.Context(), "external -- /bin/sleep 30")
		done <- err
	}()
	<-started
	deadline := time.Now().Add(10 * time.Second)
	for f.executions(t)["external"] != "running;" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	interrupted, err := f.runtime.Supervisor.Lost(t.Context(), f.session, "output:supervisor-lost")
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
	count, err := f.runtime.Supervisor.Lost(t.Context(), f.session, "output:supervisor-lost-again")
	testutil.FailErr(t, "repeat reconciliation", err)
	if count != 0 || fmt.Sprint(f.executions(t)) != fmt.Sprint(before) || before["external"] != "interrupted;" {
		t.Fatalf("loss replayed or rewrote children: before %v after %v count %d", before, f.executions(t), count)
	}
}

func TestProgramEntryDisableStopsOwnedWorkAndRefusesNewPrograms(t *testing.T) {
	evaluator := &literalEvaluator{}
	f := newProgramFixture(t, evaluator.evaluate)
	_, err := f.call(t, t.Context(), "external -- /bin/sleep 30 &")
	testutil.FailErr(t, "launch owned job", err)
	if stopped := f.runtime.SetDisabled(true); len(stopped) != 1 {
		t.Fatalf("disable stopped %v", stopped)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, stopped, err := f.runtime.Background.Lifecycle.Wait(ctx, f.session, evaluator.handles[0]); err != nil || !stopped {
		t.Fatalf("owned job after disable stopped=%v err=%v", stopped, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for f.programRows(t)[0] == "running" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	_, err = f.call(t, t.Context(), "external -- /bin/echo after")
	if rejected := toolrejection.AsToolReject(err); rejected == nil || rejected.Code != "COMMAND_PROGRAMS_DISABLED" {
		t.Fatalf("disabled entry admitted a program: %v", err)
	}
	if programs := f.programRows(t); len(programs) != 1 || programs[0] == "running" {
		t.Fatalf("programs after disable %v", programs)
	}
	f.runtime.SetDisabled(false)
	if out, err := f.call(t, t.Context(), "external -- /bin/echo again"); err != nil || !strings.Contains(out, `"stdout":"again\n"`) {
		t.Fatalf("re-enabled entry %s %v", out, err)
	}
}

func TestProgramEntryLsofReportsOnlySessionListeners(t *testing.T) {
	evaluator := &literalEvaluator{}
	f := newProgramFixture(t, evaluator.evaluate)
	var asked []string
	f.runtime.Listeners = func(_ context.Context, session string) ([]lineage.SocketObservation, error) {
		asked = append(asked, session)
		return []lineage.SocketObservation{{Local: netip.MustParseAddrPort("127.0.0.1:8080"), Listening: true}}, nil
	}
	out, err := f.call(t, t.Context(), "lsof -i :8080 -sTCP:LISTEN")
	testutil.FailErr(t, "lsof owned port", err)
	if !strings.Contains(out, `"stdout":"8080 127.0.0.1:8080 LISTEN\n"`) || !strings.Contains(out, `"status":0`) {
		t.Fatalf("lsof output %s", out)
	}
	out, err = f.call(t, t.Context(), "lsof -iTCP:9090")
	testutil.FailErr(t, "lsof unowned port", err)
	if !strings.Contains(out, `"stdout":""`) || !strings.Contains(out, `"status":1`) {
		t.Fatalf("unowned port listed %s", out)
	}
	if _, err := f.call(t, t.Context(), "lsof -i 8080"); toolrejection.AsToolReject(err) == nil {
		t.Fatalf("malformed endpoint accepted: %v", err)
	}
	if len(asked) != 2 || asked[0] != f.session || asked[1] != f.session {
		t.Fatalf("listener observation scope %v", asked)
	}
}
