//go:build unix

package toolhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

type waitStateParking struct{ parked chan *waitStatePark }
type waitStatePark struct {
	request tools.ProgramPark
	settled chan string
}

func (p waitStateParking) ParkProgram(_ context.Context, _ tools.ToolContext, request tools.ProgramPark) (tools.ParkedProgram, error) {
	if request.Claim == nil || !request.Claim() {
		return nil, tools.ErrWaitEnded
	}
	parked := &waitStatePark{request: request, settled: make(chan string, 1)}
	p.parked <- parked
	return parked, nil
}
func (p *waitStatePark) LeaseID() string                                  { return "fixture-wait" }
func (p *waitStatePark) Settle(_ context.Context, result string, _ error) { p.settled <- result }

type observedProgramWaits struct {
	port   commandprogram.ProgramWaits
	states chan commandprogram.WaitState
}

func (o observedProgramWaits) Block(ctx context.Context, state commandprogram.WaitState) (func(), error) {
	o.states <- state
	return o.port.Block(ctx, state)
}

type waitStateFixture struct {
	*pathsFixture
	clock  *programtest.ManualClock
	parked chan *waitStatePark
	states chan commandprogram.WaitState
}

func newWaitStateFixture(t *testing.T) *waitStateFixture {
	t.Helper()
	f := &waitStateFixture{pathsFixture: newPathsFixture(t), clock: programtest.NewManualClock(), parked: make(chan *waitStatePark, 4), states: make(chan commandprogram.WaitState, 64)}
	f.Runtime.Clock = f.clock
	f.Runtime.Parking = waitStateParking{parked: f.parked}
	f.Runtime.Evaluate = func(ctx context.Context, plan commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
		ports.Waits = observedProgramWaits{port: ports.Waits, states: f.states}
		return commandprogram.Evaluate(ctx, plan, ports)
	}
	return f
}
func decodeWaitOutcome(t *testing.T, content string) command.ProgramOutcome {
	t.Helper()
	var outcome command.ProgramOutcome
	testutil.FailErr(t, "decode program outcome", json.Unmarshal([]byte(content), &outcome))
	return outcome
}
func (f *waitStateFixture) call(t *testing.T, source string) string {
	t.Helper()
	content, err := f.Call(t, t.Context(), source)
	testutil.FailErr(t, "run waiting program", err)
	return content
}
func (f *waitStateFixture) start(t *testing.T, source string) <-chan string {
	t.Helper()
	done := make(chan string, 1)
	go func() { done <- f.call(t, source) }()
	return done
}
func (f *waitStateFixture) waitTimers(count int) {
	for f.clock.Pending() < count {
		runtime.Gosched()
	}
}

func TestCommandEntryRealSleepParksAndResumesWithCompleteBytes(t *testing.T) {
	f := newWaitStateFixture(t)
	content := f.call(t, "echo start; sleep 120; echo done")
	var parked command.ParkedProgramResult
	testutil.FailErr(t, "decode parked call", json.Unmarshal([]byte(content), &parked))
	if parked.State != "parked" || parked.Reason != "sleep 120" {
		t.Fatalf("parked %s", content)
	}
	pending := <-f.parked
	f.clock.Advance(119 * time.Second)
	select {
	case result := <-pending.settled:
		t.Fatalf("sleep woke early: %s", result)
	default:
	}
	f.clock.Advance(time.Second)
	outcome := decodeWaitOutcome(t, <-pending.settled)
	if outcome.Status != 0 || outcome.Stdout != "start\ndone\n" || outcome.State != "completed" {
		t.Fatalf("outcome %+v", outcome)
	}
}
func TestCommandEntryRealShortSleepStaysInline(t *testing.T) {
	f := newWaitStateFixture(t)
	done := f.start(t, "sleep 0.2; echo done")
	<-f.states
	f.clock.Advance(200 * time.Millisecond)
	outcome := decodeWaitOutcome(t, <-done)
	if outcome.Status != 0 || outcome.Stdout != "done\n" {
		t.Fatalf("outcome %+v", outcome)
	}
	select {
	case parked := <-f.parked:
		t.Fatalf("short sleep parked %+v", parked.request)
	default:
	}
}
func TestCommandEntryRealWaitUsesExactSupervisedJob(t *testing.T) {
	for _, reference := range []string{"$!", "%1", ""} {
		t.Run(reference, func(t *testing.T) {
			f := newWaitStateFixture(t)
			f.call(t, "{ sleep 120; exit 7; } & wait "+reference)
			pending := <-f.parked
			var state commandprogram.WaitState
			for len(state.Jobs) == 0 {
				state = <-f.states
			}
			active := f.Background.ActiveJobs(f.Session)
			if len(active) != 1 || len(state.Jobs) != 1 || state.Jobs[0] != active[0].Handle || !strings.HasPrefix(state.Statement, "wait") {
				t.Fatalf("wait %+v jobs %+v", state, active)
			}
			f.clock.Advance(120 * time.Second)
			outcome := decodeWaitOutcome(t, <-pending.settled)
			if outcome.Status != 7 || outcome.State != "completed" {
				t.Fatalf("outcome %+v", outcome)
			}
		})
	}
}
func TestCommandEntryRealTimeoutParksAndSettlesItsChild(t *testing.T) {
	f := newWaitStateFixture(t)
	content := f.call(t, "timeout 60 sleep 120")
	pending := <-f.parked
	if !strings.Contains(content, "timeout 60 sleep 120") {
		t.Fatalf("reason %s", content)
	}
	f.clock.Advance(60 * time.Second)
	outcome := decodeWaitOutcome(t, <-pending.settled)
	if outcome.Status != 124 || outcome.State != "completed" {
		t.Fatalf("outcome %+v", outcome)
	}
	if active := f.Background.ActiveJobs(f.Session); len(active) != 0 {
		t.Fatalf("timeout left jobs %+v", active)
	}
	if f.clock.Pending() != 0 {
		t.Fatalf("timeout left %d timers", f.clock.Pending())
	}
}
func TestCommandEntryTimedReadDoesNotWakeSleepEarly(t *testing.T) {
	f := newWaitStateFixture(t)
	done := f.start(t, "sleep 120 | read -t 5 value")
	f.waitTimers(2)
	f.clock.Advance(5 * time.Second)
	pending := <-f.parked
	if result := <-done; !strings.Contains(result, "parked") {
		t.Fatalf("remaining timer did not park %s", result)
	}
	f.clock.Advance(114 * time.Second)
	select {
	case result := <-pending.settled:
		t.Fatalf("read timeout woke sleep early %s", result)
	default:
	}
	f.clock.Advance(time.Second)
	outcome := decodeWaitOutcome(t, <-pending.settled)
	if outcome.Status != 142 || outcome.State != "completed" {
		t.Fatalf("outcome %+v", outcome)
	}
}
func TestCommandEntryBackgroundOnlyReturnsAndItsTimerSurvives(t *testing.T) {
	f := newWaitStateFixture(t)
	outcome := decodeWaitOutcome(t, f.call(t, "{ sleep 120; echo done; } &"))
	if outcome.State != "running" || outcome.Status != 0 {
		t.Fatalf("background outcome %+v", outcome)
	}
	f.waitTimers(1)
	select {
	case parked := <-f.parked:
		t.Fatalf("background-only call parked %+v", parked.request)
	default:
	}
	active := f.Background.ActiveJobs(f.Session)
	if len(active) != 1 {
		t.Fatalf("active jobs %+v", active)
	}
	f.clock.Advance(120 * time.Second)
	_, stopped, err := f.Background.Lifecycle.Wait(t.Context(), f.Session, active[0].Handle)
	testutil.FailErr(t, "wait background completion", err)
	if stopped {
		t.Fatal("returning from the foreground stopped its background job")
	}
}
func TestCommandEntryRealSleepRefusesBeyondEnvelopeBeforeParking(t *testing.T) {
	f := newWaitStateFixture(t)
	outcome := decodeWaitOutcome(t, f.call(t, "sleep 7200"))
	if outcome.Refusal == nil || outcome.Refusal.Code != "COMMAND_LIMIT_EXCEEDED" {
		t.Fatalf("bound %+v", outcome)
	}
	select {
	case parked := <-f.parked:
		t.Fatalf("over-bound wait parked %+v", parked.request)
	default:
	}
}

func TestCommandEntryTimedReadPreservesPartialInputAndInheritedDescriptor(t *testing.T) {
	f := newWaitStateFixture(t)
	done := f.start(t, `{ printf part; sleep 120; printf '\nnext\n'; } | { read -t 5 first; status=$?; read second; read third; printf '%s:%s:%s:%s\n' "$status" "$first" "$second" "$third"; }`)
	var state commandprogram.WaitState
	for state.Statement != "read -t 5 first" {
		state = <-f.states
	}
	f.waitTimers(2)
	f.clock.Advance(5 * time.Second)
	pending := <-f.parked
	<-done
	f.clock.Advance(115 * time.Second)
	outcome := decodeWaitOutcome(t, <-pending.settled)
	if outcome.State != "completed" || outcome.Status != 0 || outcome.Stdout != "142:part::next\n" {
		t.Fatalf("read reuse %+v", outcome)
	}
}
func TestCommandEntryTimedReadValidatesAndPollsWithoutConsuming(t *testing.T) {
	for _, tc := range []struct {
		program, stdout, stderr string
		status                  int
	}{
		{`read -t invalid value`, "", "read: invalid: invalid timeout specification\n", 1},
		{`value=kept; read -t 0 value <<< ready; printf '%s:%s\n' "$?" "$value"`, "0:kept\n", "", 0},
		{`value=kept; read -t 0 value; printf '%s:%s\n' "$?" "$value"`, "0:kept\n", "", 0},
	} {
		t.Run(tc.program, func(t *testing.T) {
			f := newWaitStateFixture(t)
			outcome := decodeWaitOutcome(t, f.call(t, tc.program))
			if outcome.Status != tc.status || outcome.Stdout != tc.stdout || outcome.Stderr != tc.stderr || outcome.Refusal != nil {
				t.Fatalf("read %+v", outcome)
			}
		})
	}
}
func TestCommandEntryPollingLoopRunsProbesBetweenFullSleeps(t *testing.T) {
	f := newWaitStateFixture(t)
	done := f.start(t, `{ sleep 12; touch ready; } & until test -f ready; do sleep 5; done; echo done`)
	for range 2 {
		for {
			state := <-f.states
			if state.Until.Equal(f.clock.Now().Add(5 * time.Second)) {
				break
			}
		}
		f.clock.Advance(5 * time.Second)
	}
	pending := <-f.parked
	<-done
	f.clock.Advance(2 * time.Second)
	for {
		state := <-f.states
		if state.Until.Equal(f.clock.Now().Add(3 * time.Second)) {
			break
		}
	}
	select {
	case result := <-pending.settled:
		t.Fatalf("readiness woke sleep early: %s", result)
	default:
	}
	f.clock.Advance(3 * time.Second)
	outcome := decodeWaitOutcome(t, <-pending.settled)
	if outcome.Status != 0 || outcome.Stdout != "done\n" {
		t.Fatalf("readiness loop %+v", outcome)
	}
}

func TestCommandWaitingCatalogGoldenPrograms(t *testing.T) {
	catalog, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "command catalog", err)
	var contract struct {
		Golden []struct {
			ID, Program string
			Expected    struct {
				Stdout, Stderr string
				Status         int
			}
		} `json:"golden_programs"`
	}
	testutil.FailErr(t, "decode waiting goldens", json.Unmarshal(catalog.Contracts["golden"], &contract))
	for _, golden := range contract.Golden {
		if golden.ID != "read-timer-zero-ready" && golden.ID != "read-timer-invalid" {
			continue
		}
		t.Run(golden.ID, func(t *testing.T) {
			f := newWaitStateFixture(t)
			got := decodeWaitOutcome(t, f.call(t, golden.Program))
			want := golden.Expected
			if got.Status != want.Status || got.Stdout != want.Stdout || got.Stderr != want.Stderr || got.Refusal != nil {
				t.Fatalf("golden result %+v", got)
			}
		})
	}
}

func TestCommandEntryParallelXargsTracksEachLiveActivity(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		completed   int
	}{
		{"all children waiting", "120\n120\n", 0},
		{"second child runnable", "120\n0\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWaitStateFixture(t)
			program := "printf '" + strings.ReplaceAll(tc.input, "\n", `\n`) + "' | xargs -n1 -P2 sleep; echo done"
			f.call(t, program)
			pending := <-f.parked
			completed := 0
			for operation, states := range f.Executions(t) {
				if strings.Contains(operation, "sleep") {
					completed += strings.Count(states, "completed;")
				}
			}
			if completed != tc.completed {
				t.Fatalf("completed sleep children %d, want %d", completed, tc.completed)
			}
			f.clock.Advance(120 * time.Second)
			outcome := decodeWaitOutcome(t, <-pending.settled)
			if outcome.Status != 0 || outcome.State != "completed" || outcome.Stdout != "done\n" {
				t.Fatalf("parallel wait %+v", outcome)
			}
		})
	}
}

func TestCommandEntryCompletedOrEmptyWaitStaysInlinePastTheThreshold(t *testing.T) {
	for _, source := range []string{"sleep 0.2; wait", "true & sleep 0.2; wait %1"} {
		t.Run(source, func(t *testing.T) {
			f := newWaitStateFixture(t)
			done := f.start(t, source)
			f.waitTimers(1)
			for len(f.Background.ActiveJobs(f.Session)) > 0 {
				runtime.Gosched()
			}
			f.clock.Advance(20 * time.Second)
			outcome := decodeWaitOutcome(t, <-done)
			if outcome.Status != 0 {
				t.Fatalf("inline wait %+v", outcome)
			}
			select {
			case parked := <-f.parked:
				t.Fatalf("ready wait parked %+v", parked.request)
			default:
			}
		})
	}
}

func TestCommandWaitingExternalHelper(t *testing.T) {
	if os.Getenv("PW_COMMAND_WAIT_HELPER") != "1" {
		return
	}
	if err := os.WriteFile("running", []byte("ready"), 0o600); err != nil {
		os.Exit(2)
	}
	for {
		if _, err := os.Stat("release"); err == nil {
			break
		}
		runtime.Gosched()
	}
	fmt.Print("external done\n")
	os.Exit(7)
}
func (f *waitStateFixture) externalHelper() string {
	return "PW_COMMAND_WAIT_HELPER=1 external -- " + strconv.Quote(f.helper) + " '-test.run=^TestCommandWaitingExternalHelper$'"
}
func (f *waitStateFixture) awaitExternal() {
	for {
		if _, err := os.Stat(filepath.Join(f.Dir, "running")); err == nil {
			return
		}
		runtime.Gosched()
	}
}
func TestCommandEntryOrdinaryExternalRemainsRunnableBesideBackgroundSleep(t *testing.T) {
	f := newWaitStateFixture(t)
	done := f.start(t, "sleep 120 & "+f.externalHelper())
	f.awaitExternal()
	f.waitTimers(1)
	f.clock.Advance(20 * time.Second)
	select {
	case parked := <-f.parked:
		t.Fatalf("running external process parked %+v", parked.request)
	default:
	}
	testutil.FailErr(t, "release external fixture", os.WriteFile(filepath.Join(f.Dir, "release"), nil, 0o600))
	outcome := decodeWaitOutcome(t, <-done)
	if outcome.Status != 7 || outcome.Stdout != "external done\n" {
		t.Fatalf("external result %+v", outcome)
	}
	f.clock.Advance(100 * time.Second)
}
func TestCommandEntryExactExternalJobWaitParksAndResumes(t *testing.T) {
	f := newWaitStateFixture(t)
	done := f.start(t, f.externalHelper()+" & wait $!")
	f.awaitExternal()
	f.waitTimers(1)
	f.clock.Advance(10 * time.Second)
	pending := <-f.parked
	if got := <-done; !strings.Contains(got, "parked") {
		t.Fatalf("job wait result %s", got)
	}
	testutil.FailErr(t, "complete external job", os.WriteFile(filepath.Join(f.Dir, "release"), nil, 0o600))
	outcome := decodeWaitOutcome(t, <-pending.settled)
	if outcome.Status != 7 || outcome.State != "completed" {
		t.Fatalf("external job %+v", outcome)
	}
}
func TestCommandEntryExternalTimeoutParksAndReapsTheChild(t *testing.T) {
	f := newWaitStateFixture(t)
	done := f.start(t, strings.Replace(f.externalHelper(), " external -- ", " timeout 60 external -- ", 1))
	f.awaitExternal()
	pending := <-f.parked
	<-done
	f.clock.Advance(60 * time.Second)
	outcome := decodeWaitOutcome(t, <-pending.settled)
	if outcome.Status != 124 || outcome.State != "completed" {
		t.Fatalf("external timeout %+v", outcome)
	}
}

func TestCommandEntryAwaitedBackgroundFramesKeepTheExactJobIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"pipeline", "HELPER | cat", 7},
		{"process substitution", "cat <(HELPER)", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWaitStateFixture(t)
			body := strings.ReplaceAll(tc.body, "HELPER", f.externalHelper())
			done := f.start(t, "{ "+body+"; } & wait $!")
			f.awaitExternal()
			f.waitTimers(1)
			f.clock.Advance(10 * time.Second)
			pending := <-f.parked
			<-done
			testutil.FailErr(t, "release background external fixture", os.WriteFile(filepath.Join(f.Dir, "release"), nil, 0o600))
			outcome := decodeWaitOutcome(t, <-pending.settled)
			if outcome.State != "completed" || outcome.Status != tc.status {
				t.Fatalf("background frame %+v", outcome)
			}
		})
	}
}

func TestCommandEntryBoundRefusalKeepsDetachedJobsAlive(t *testing.T) {
	f := newWaitStateFixture(t)
	outcome := decodeWaitOutcome(t, f.call(t, "sleep 7200 & wait $!"))
	if outcome.Refusal == nil || outcome.Refusal.Code != "COMMAND_LIMIT_EXCEEDED" {
		t.Fatalf("bound refusal %+v", outcome)
	}
	if !f.Background.HasRunning(f.Session) || f.clock.Pending() != 1 {
		t.Fatalf("foreground refusal stopped detached work; timers=%d", f.clock.Pending())
	}
	select {
	case parked := <-f.parked:
		t.Fatalf("over-bound wait parked %+v", parked.request)
	default:
	}
}
