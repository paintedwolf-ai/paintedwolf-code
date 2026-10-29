//go:build unix

package toolhost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

// supervisionClock fires program timers only when a test fires them; every
// scheduled timer is an observed event.
type supervisionTimer struct {
	after time.Duration
	fire  func()
}

type supervisionClock struct {
	mu        sync.Mutex
	timers    []func()
	scheduled chan supervisionTimer
}

func newSupervisionClock() *supervisionClock {
	return &supervisionClock{scheduled: make(chan supervisionTimer, 8)}
}

func (c *supervisionClock) Now() time.Time { return time.Unix(1_000_000, 0) }

func (c *supervisionClock) AfterFunc(d time.Duration, fn func()) func() bool {
	stopped := false
	var fire func()
	c.mu.Lock()
	fire = func() {
		c.mu.Lock()
		run := !stopped
		c.mu.Unlock()
		if run {
			fn()
		}
	}
	c.timers = append(c.timers, fire)
	c.mu.Unlock()
	c.scheduled <- supervisionTimer{after: d, fire: fire}
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !stopped
		stopped = true
		return was
	}
}

// fireNext waits for the next timer the program schedules and fires it.
func (c *supervisionClock) fireNext(t *testing.T, want time.Duration) {
	t.Helper()
	event := <-c.scheduled
	if event.after != want {
		t.Fatalf("scheduled %v, want %v", event.after, want)
	}
	event.fire()
}

type programCall struct {
	outcome command.ProgramOutcome
	err     error
}

func callAsync(t *testing.T, ctx context.Context, f *pathsFixture, program string) <-chan programCall {
	done := make(chan programCall, 1)
	go func() {
		raw, err := f.Call(t, ctx, program)
		var out command.ProgramOutcome
		if err == nil {
			err = json.Unmarshal([]byte(raw), &out)
		}
		done <- programCall{out, err}
	}()
	return done
}

// awaitFile waits for a file a launched process creates once it is ready.
func awaitFile(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s never appeared", path)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestCommandEntryTimeoutEndsTheInnerCommandWith124(t *testing.T) {
	for _, tc := range []struct{ name, program, stdout string }{
		{"native sleep", `timeout 1 sleep 5; echo "status $?"`, "status 124\n"},
		{"native sleep preserving status", `timeout --preserve-status 1 sleep 5; echo "status $?"`, "status 143\n"},
		{"external sleep", `timeout 1 /bin/sleep 5; echo "status $?"`, "status 124\n"},
		{"external sleep preserving status", `timeout -s INT --preserve-status 1 /bin/sleep 5; echo "status $?"`, "status 130\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPathsFixture(t)
			clock := newSupervisionClock()
			f.Runtime.Clock = clock
			done := callAsync(t, t.Context(), f, tc.program)
			clock.fireNext(t, time.Second)
			got := <-done
			// The program only settles once the inner command is gone.
			if got.err != nil || got.outcome.Status != 0 || got.outcome.Stdout != tc.stdout || got.outcome.Refusal != nil {
				t.Fatalf("timeout outcome: %+v, %v", got.outcome, got.err)
			}
			if f.Background.HasRunning(f.Session) {
				t.Fatal("the timed-out command left a running job")
			}
		})
	}
}

// A process that ignores TERM gets KILL after -k; the verbose report names
// both deliveries in order.
func TestCommandEntryTimeoutKillAfterEscalates(t *testing.T) {
	for _, tc := range []struct{ flags, status string }{{"", "124"}, {"--preserve-status ", "137"}} {
		t.Run("status "+tc.status, func(t *testing.T) {
			f := newPathsFixture(t)
			clock := newSupervisionClock()
			f.Runtime.Clock = clock
			ready := filepath.Join(f.Dir, "ready")
			done := callAsync(t, t.Context(), f, `timeout -v `+tc.flags+`-k 2 1 /bin/sh -c 'trap "" TERM; : > ready; while :; do :; done'; echo "status $?"`)
			<-clock.scheduled
			awaitFile(t, t.Context(), ready)
			clock.mu.Lock()
			expire := clock.timers[0]
			clock.mu.Unlock()
			expire()
			clock.fireNext(t, 2*time.Second)
			got := <-done
			if got.err != nil || got.outcome.Stdout != "status "+tc.status+"\n" {
				t.Fatalf("kill-after outcome: %+v, %v", got.outcome, got.err)
			}
			if want := "timeout: sending signal TERM to command '/bin/sh'\ntimeout: sending signal KILL to command '/bin/sh'\n"; got.outcome.Stderr != want {
				t.Fatalf("verbose report = %q, want %q", got.outcome.Stderr, want)
			}
		})
	}
}

// The command envelope's own bound wins over a longer timeout.
func TestCommandEntryTimeoutLeavesTheOuterBoundInCharge(t *testing.T) {
	f := newPathsFixture(t)
	clock := newSupervisionClock()
	f.Runtime.Clock = clock
	ctx, cancel := context.WithCancel(t.Context())
	done := callAsync(t, ctx, f, `timeout 600 /bin/sleep 30; echo "status $?"`)
	<-clock.scheduled
	cancel()
	got := <-done
	if !errors.Is(got.err, context.Canceled) || strings.Contains(got.outcome.Stdout, "124") {
		t.Fatalf("outer bound outcome: %+v, %v; want the call interrupted, not status 124", got.outcome, got.err)
	}
	if programs := f.Programs(t); len(programs) != 1 || programs[0] != "interrupted" {
		t.Fatalf("program receipts %v, want one interrupted program", programs)
	}
}

// The inner command dispatches exactly as without timeout: the same
// confined launch, effects and status.
func TestCommandEntryTimeoutRunsTheChildUnderTheSameConfinement(t *testing.T) {
	outside := t.TempDir()
	script := `/bin/sh -c 'echo in > inside.txt; echo out > ` + outside + `/outside.txt; echo done'; echo "status $?"; cat inside.txt`
	var results []command.ProgramOutcome
	for _, prefix := range []string{"", "timeout 5 "} {
		f := newPathsFixture(t)
		raw, err := f.Call(t, t.Context(), prefix+script)
		testutil.FailErr(t, "run confined child", err)
		got := decodeProgramOutcome(t, raw)
		results = append(results, got)
		if executions := f.Executions(t); !strings.Contains(executions["command"], "completed") {
			t.Fatalf("%q: no confined launch settled: %v", prefix, executions)
		}
		_ = os.Remove(filepath.Join(outside, "outside.txt"))
	}
	if results[0].Stdout != results[1].Stdout || results[0].Stderr != results[1].Stderr || results[0].Status != results[1].Status {
		t.Fatalf("timeout changed the child's confinement:\nplain   %+v\ntimeout %+v", results[0], results[1])
	}
}

func TestCommandEntryTimeoutUsageErrors(t *testing.T) {
	f := newPathsFixture(t)
	raw, err := f.Call(t, t.Context(), `timeout 5; echo "a $?"; timeout soon true; echo "b $?"; timeout -s BOGUS 1 true; echo "c $?"; timeout 0 printf 'ran\n'`)
	testutil.FailErr(t, "run timeout usage errors", err)
	got := decodeProgramOutcome(t, raw)
	if got.Stdout != "a 125\nb 125\nc 125\nran\n" || !strings.Contains(got.Stderr, "timeout: missing operand") || !strings.Contains(got.Stderr, "timeout: invalid time interval 'soon'") || !strings.Contains(got.Stderr, "timeout: BOGUS: invalid signal") {
		t.Fatalf("usage outcome: %+v", got)
	}
}

func TestCommandEntryTimePOSIXReportOnStderr(t *testing.T) {
	f := newPathsFixture(t)
	f.Runtime.Clock = newSupervisionClock()
	raw, err := f.Call(t, t.Context(), "time -p true")
	testutil.FailErr(t, "run time -p", err)
	got := decodeProgramOutcome(t, raw)
	if got.Status != 0 || got.Stdout != "" || got.Stderr != "real 0.00\nuser 0.00\nsys 0.00\n" || got.CPUTimeUnmeasured {
		t.Fatalf("time -p outcome: %+v", got)
	}
	raw, err = f.Call(t, t.Context(), "time /usr/bin/true; time -p wc -c <<< abc")
	testutil.FailErr(t, "run time with a process and a native", err)
	got = decodeProgramOutcome(t, raw)
	report := regexp.MustCompile(`^\nreal\t0m0\.000s\nuser\t\d+m\d+\.\d{3}s\nsys\t\d+m\d+\.\d{3}s\nreal 0\.00\nuser 0\.00\nsys 0\.00\n$`)
	if got.Status != 0 || got.Stdout != "4\n" || !report.MatchString(got.Stderr) || !got.CPUTimeUnmeasured {
		t.Fatalf("time outcome: %+v", got)
	}
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	testutil.FailErr(t, "write script", os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755))
}

// nohup starts an ordinary session job that survives SIGHUP; its output is
// read with command_output and nothing is written to nohup.out.
func TestCommandEntryNohupBackgroundJob(t *testing.T) {
	f := newPathsFixture(t)
	writeScript(t, filepath.Join(f.Dir, "server"), "kill -HUP $$\necho listening\necho diagnostics >&2\nwhile [ ! -f stop ]; do sleep 0.05; done\necho bye\n")
	raw, err := f.Call(t, t.Context(), "nohup ./server &")
	testutil.FailErr(t, "start nohup job", err)
	if got := decodeProgramOutcome(t, raw); got.Status != 0 || got.Refusal != nil {
		t.Fatalf("nohup start outcome: %+v", got)
	}
	jobs := f.Background.ActiveJobs(f.Session)
	if len(jobs) != 1 {
		t.Fatalf("active jobs = %+v, want the nohup job", jobs)
	}
	handle := jobs[0].Handle
	output := &command.CommandOutputTool{Registry: f.Background}
	var tc tools.ToolContext
	tc.Identity.SessionID, tc.Identity.ProjectID = f.Session, testdbseed.DefaultProjectID
	for {
		raw, err := output.Run(t.Context(), map[string]any{"handle": handle}, tc)
		testutil.FailErr(t, "read job output", err)
		if strings.Contains(raw, "listening") {
			if strings.Contains(raw, "diagnostics") {
				break
			}
		}
		if !strings.Contains(raw, `"running":true`) {
			t.Fatalf("job ended before it listened (SIGHUP was not ignored): %s", raw)
		}
		select {
		case <-t.Context().Done():
			t.Fatal("job output never arrived")
		case <-time.After(10 * time.Millisecond):
		}
	}
	testutil.FailErr(t, "stop server", os.WriteFile(filepath.Join(f.Dir, "stop"), nil, 0o600))
	status, _, err := f.Background.Lifecycle.Wait(t.Context(), f.Session, handle)
	if err != nil || status != 0 {
		t.Fatalf("job status %d, %v", status, err)
	}
	if _, statErr := os.Stat(filepath.Join(f.Dir, "nohup.out")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("nohup.out was written: %v", statErr)
	}
	// Without nohup the same SIGHUP ends the script.
	raw, err = f.Call(t, t.Context(), `./server; echo "status $?"`)
	testutil.FailErr(t, "run without nohup", err)
	if got := decodeProgramOutcome(t, raw); got.Stdout != "status 129\n" {
		t.Fatalf("plain run outcome: %+v", got)
	}
}

func TestCommandEntryNohupRefusesNohupOutAndMissingCommand(t *testing.T) {
	f := newPathsFixture(t)
	source := "nohup printf x > nohup.out"
	raw, err := f.Call(t, t.Context(), source)
	testutil.FailErr(t, "run nohup.out redirect", err)
	got := decodeProgramOutcome(t, raw)
	if got.Refusal == nil || got.Refusal.Code != "COMMAND_OPTION_UNSUPPORTED" || got.Refusal.Details["word"] != "nohup.out" {
		t.Fatalf("nohup.out outcome: %+v", got)
	}
	if _, statErr := os.Stat(filepath.Join(f.Dir, "nohup.out")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused redirect still published nohup.out: %v", statErr)
	}
	raw, err = f.Call(t, t.Context(), `nohup; echo "status $?"`)
	testutil.FailErr(t, "run bare nohup", err)
	if got := decodeProgramOutcome(t, raw); got.Stdout != "status 125\n" || got.Stderr != "nohup: missing operand\nTry 'nohup --help' for more information.\n" {
		t.Fatalf("bare nohup outcome: %+v", got)
	}
}

// ulimit reports what external children receive, a lowered limit reaches
// later children of the same program, and the next call starts fresh.
func TestCommandEntryUlimitReachesLaterExternalChildrenOnly(t *testing.T) {
	f := newPathsFixture(t)
	probe := `external -- /bin/sh -c 'ulimit -n'`
	raw, err := f.Call(t, t.Context(), "ulimit -n; "+probe)
	testutil.FailErr(t, "report the child limit", err)
	got := decodeProgramOutcome(t, raw)
	lines := strings.Split(strings.TrimSuffix(got.Stdout, "\n"), "\n")
	if got.Status != 0 || len(lines) != 2 || lines[0] != lines[1] {
		t.Fatalf("ulimit -n differs from the child's own: %+v", got)
	}
	raw, err = f.Call(t, t.Context(), "ulimit -n 256; "+probe)
	testutil.FailErr(t, "lower the child limit", err)
	if got := decodeProgramOutcome(t, raw); got.Stdout != "256\n" {
		t.Fatalf("lowered limit outcome: %+v", got)
	}
	raw, err = f.Call(t, t.Context(), probe)
	testutil.FailErr(t, "next call", err)
	if got := decodeProgramOutcome(t, raw); got.Stdout == "256\n" {
		t.Fatalf("the limit persisted into the next call: %+v", got)
	}
}

// A lowered hard limit reaches later external children of the same program.
func TestCommandEntryUlimitHardLimitReachesLaterChildren(t *testing.T) {
	f := newPathsFixture(t)
	raw, err := f.Call(t, t.Context(), `ulimit -S -n 64; ulimit -H -n 64; external -- /bin/sh -c 'ulimit -Hn; ulimit -Sn'`)
	testutil.FailErr(t, "lower the hard limit", err)
	if got := decodeProgramOutcome(t, raw); got.Status != 0 || got.Refusal != nil || got.Stdout != "64\n64\n" {
		t.Fatalf("hard limit outcome: %+v", got)
	}
}

// TestCatalogGoldenProgramsForProgramPrefixes runs the catalog's golden
// programs for timeout, time, nohup and ulimit through the composed entry.
func TestCatalogGoldenProgramsForProgramPrefixes(t *testing.T) {
	catalog, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "catalog", err)
	var contract struct {
		Golden []struct {
			ID       string `json:"id"`
			Program  string `json:"program"`
			Expected struct {
				Stdout     string  `json:"stdout"`
				Status     int     `json:"status"`
				Diagnostic *string `json:"diagnostic"`
				Span       *struct {
					Start float64 `json:"start_byte"`
					End   float64 `json:"end_byte"`
				} `json:"source_span"`
			} `json:"expected"`
		} `json:"golden_programs"`
	}
	testutil.FailErr(t, "golden contract", json.Unmarshal(catalog.Contracts["golden"], &contract))
	ran := map[string]bool{}
	for _, golden := range contract.Golden {
		switch golden.ID {
		case "timeout-prefix", "time-pipeline", "nohup-prefix", "nohup-output-refused", "ulimit-lower-soft", "ulimit-hard-lowered":
		default:
			continue
		}
		ran[golden.ID] = true
		t.Run(golden.ID, func(t *testing.T) {
			f := newPathsFixture(t)
			out, err := f.Call(t, t.Context(), golden.Program)
			testutil.FailErr(t, "run golden program", err)
			got := decodeProgramOutcome(t, out)
			if got.Status != golden.Expected.Status || got.Stdout != golden.Expected.Stdout {
				t.Fatalf("golden outcome: %+v", got)
			}
			if golden.Expected.Diagnostic == nil {
				if got.Refusal != nil {
					t.Fatalf("unexpected refusal: %+v", got.Refusal)
				}
				return
			}
			if got.Refusal == nil || got.Refusal.Code != *golden.Expected.Diagnostic || golden.Expected.Span == nil ||
				got.Refusal.Details["source_start_byte"] != golden.Expected.Span.Start || got.Refusal.Details["source_end_byte"] != golden.Expected.Span.End {
				t.Fatalf("refusal %+v, want %s at %+v", got.Refusal, *golden.Expected.Diagnostic, golden.Expected.Span)
			}
		})
	}
	if len(ran) != 6 {
		t.Fatalf("golden programs exercised: %v", ran)
	}
}

var _ commandprogram.Clock = (*supervisionClock)(nil)
