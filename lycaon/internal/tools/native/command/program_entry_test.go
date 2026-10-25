package command_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
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
	_, err := f.call(t, t.Context(), "external -- /bin/echo x | tee first.txt; cat <(ls)")
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
	_, err = f.call(t, t.Context(), "kill -TERM "+handle)
	testutil.FailErr(t, "kill session job", err)
	jobs := &command.SessionJobs{Registry: f.Background, SessionID: f.Session}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	exit, stopped, err := f.Background.Lifecycle.Wait(ctx, f.Session, handle)
	if err != nil || !stopped || exit == 0 {
		t.Fatalf("killed job exit=%d stopped=%v err=%v", exit, stopped, err)
	}
	if live, _ := jobs.List(t.Context()); len(live) != 0 {
		t.Fatalf("killed job still listed %v", live)
	}
	for _, reference := range []string{"not-a-session-job", "%2", "4242"} {
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
	handle := f.jobHandle(t)
	if stopped := f.Runtime.SetDisabled(true); len(stopped) != 1 {
		t.Fatalf("disable stopped %v", stopped)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, stopped, err := f.Background.Lifecycle.Wait(ctx, f.Session, handle); err != nil || !stopped {
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
	if programs := f.programRows(t); len(programs) != 2 || programs[0] != "interrupted" || programs[1] != "completed" {
		t.Fatalf("programs after disable %v", programs)
	}
	f.Runtime.SetDisabled(false)
	if out, err := f.call(t, t.Context(), "external -- /bin/echo again"); err != nil || !strings.Contains(out, `"stdout":"again\n"`) {
		t.Fatalf("re-enabled entry %s %v", out, err)
	}
}

func TestProgramEntryLsofReportsOnlySessionListeners(t *testing.T) {
	f := newProgramFixture(t)
	var asked []string
	f.Runtime.Listeners = func(_ context.Context, session string) ([]lineage.SocketObservation, error) {
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
	out, err = f.call(t, t.Context(), "lsof -i 8080")
	testutil.FailErr(t, "lsof malformed endpoint", err)
	if decodeOutcome(t, out).Refusal == nil {
		t.Fatalf("malformed endpoint accepted: %s", out)
	}
	if len(asked) != 2 || asked[0] != f.Session || asked[1] != f.Session {
		t.Fatalf("listener observation scope %v", asked)
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
	if outcome.Stdout != "before\n" || outcome.ProgramID == "" || !strings.Contains(outcome.Stderr, "repo-map: not supported here [COMMAND_NATIVE_FORM_UNSUPPORTED]") {
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
	if !strings.Contains(outcome.Stderr, "external: not supported here [COMMAND_EXTERNAL_HIDDEN]") {
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
		f.Configure = func(tc *tools.ToolContext) { tc.Effects.Out = out }
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

type testLoopbackGate struct{}

func (testLoopbackGate) Await(_ context.Context, ask tools.LoopbackConnectAsk) (tools.LoopbackConnectResult, error) {
	return tools.LoopbackConnectResult{Authorized: true, Ports: ask.Ports}, nil
}

func (testLoopbackGate) SessionLoopbackGrant(context.Context, string, string) (bool, []uint16) {
	return true, []uint16{8080}
}

func TestProgramEntryForwardsSupportedCapabilityDeclarationsToNativeChildren(t *testing.T) {
	f := newProgramFixture(t)
	testutil.FailErr(t, "register http_request owner", f.Runtime.Registry.Register("http_request", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return "", nil
	}))
	classes, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandNetworkEgress, toolcontract.CommandExternalExec)
	testutil.FailErr(t, "compile profile effects", err)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "fixture", CommandEffects: classes}})
	f.Runtime.Boundary = boundary
	f.Runtime.Executor = toolexecution.NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), f.Runtime.Registry, "fixture")
	f.Runtime.Executor.Capabilities.SetLoopbackConnectGate(testLoopbackGate{})
	var curlGranted bool
	var curlPorts []uint16
	curlOp := command.NativeOperation{Tool: "http_request", Prepare: func(_ context.Context, _ *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
		bound, err := commandprogram.CatalogBinder{}.Bind(catalogSpec("curl"), request.Argv)
		if err != nil {
			return command.NativePrepared{}, err
		}
		return command.NativePrepared{Mode: "request", Review: bound.Review("http_request"), Invoke: func(_ context.Context, tc tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
			curlGranted = tc.Local.LoopbackConnectGranted
			curlPorts = append([]uint16(nil), tc.Local.LoopbackConnectPorts...)
			_, err := io.WriteString(streams.Output, "ok\n")
			if err != nil {
				return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
			}
			return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.EffectCompleted}, nil
		}}, nil
	}}
	f.Runtime.Owners = func(tools.ToolContext, *projectpaths.ProgramPaths) (command.ProgramOwners, error) {
		return command.ProgramOwners{Natives: map[string]command.NativeOperation{
			"curl": curlOp,
			"tee":  teeOperation(f.Dir),
			"head": firstLineOperation(),
		}}, nil
	}
	args := map[string]any{
		"command": "curl http://127.0.0.1:8080/health | tee out.txt | head",
		"capability_request": map[string]any{
			"local_listen":     map[string]any{"ports": []any{9090}},
			"loopback_connect": map[string]any{"ports": []any{8080}},
		},
	}
	contract, _ := toolcontract.Lookup("command")
	parent, err := f.Recorder.Begin(t.Context(), invocation.Start{ProjectID: testdbseed.DefaultProjectID, SessionID: f.Session, ToolCallID: "model-call-cap", ToolName: "command", Args: args, Contract: contract})
	testutil.FailErr(t, "begin model receipt", err)
	tc := tools.ToolContext{Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: f.Dir, IsPrimary: true}}, ActiveRootID: "r1", SourceWorkspaceKind: api.SourceWorkspaceKindProject}}
	tc.Identity.Agent, tc.Identity.ProjectID, tc.Identity.SessionID, tc.Identity.ToolCallID = "fixture", testdbseed.DefaultProjectID, f.Session, "model-call-cap"
	tc.Invocation.ID = parent.ID
	out, err := f.Runtime.Run(t.Context(), args, tc)
	testutil.FailErr(t, "run capability-forwarded pipeline", err)
	outcome := decodeOutcome(t, out)
	if outcome.State != "completed" || outcome.Status != 0 || outcome.Stdout != "ok\n" {
		t.Fatalf("pipeline outcome: %+v", outcome)
	}
	if !curlGranted || len(curlPorts) != 1 || curlPorts[0] != 8080 {
		t.Fatalf("curl loopback grant = (%v, %v), want (true, [8080])", curlGranted, curlPorts)
	}
	executions := f.executions(t)
	if executions["http_request"] != "completed;" || executions["write"] != "completed;" || executions["read"] != "completed;" {
		t.Fatalf("child executions = %v", executions)
	}
}
