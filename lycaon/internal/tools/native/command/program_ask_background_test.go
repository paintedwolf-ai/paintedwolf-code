package command_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type askBarrierFixture struct {
	*programtest.Fixture
	clock   *programtest.ManualClock
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	askMode bool
}

func newAskBarrierFixture(t *testing.T, initialLine string) *askBarrierFixture {
	t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	base := programtest.New(t, programtest.Options{
		Natives: func(dir string) map[string]command.NativeOperation {
			op := firstLineOperation()
			prepare := op.Prepare
			op.Prepare = func(ctx context.Context, paths *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				prepared, err := prepare(ctx, paths, dispatch, request)
				if err != nil {
					return command.NativePrepared{}, err
				}
				prepared.Invoke = func(ctx context.Context, _ tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
					_, _ = fmt.Fprintln(streams.Output, initialLine)
					enteredOnce.Do(func() { close(entered) })
					select {
					case <-release:
					case <-ctx.Done():
						return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.NoEffect}, ctx.Err()
					}
					return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
				}
				return prepared, nil
			}
			return map[string]command.NativeOperation{
				"head": op,
				"tee":  teeOperation(dir),
			}
		},
	})
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint catalog", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	base.Runtime.Executor.Rejections.SetRejectFormatter(guidance.NewStaticRejectFormatter(hints))
	clock := programtest.NewManualClock()
	base.Runtime.Clock = clock

	f := &askBarrierFixture{
		Fixture: base,
		clock:   clock,
		entered: entered,
		release: release,
		askMode: true,
	}
	base.Configure = func(tc *tools.ToolContext) {
		f.mu.Lock()
		ask := f.askMode
		f.mu.Unlock()
		tc.Source.AskMode = ask
		tc.Source.ReadOnly = ask
	}
	return f
}

// setAskMode toggles the future ToolContext mode for subsequent foreground
// command invocations on this runtime fixture. Background jobs already launched
// retain their frozen launch-time programRun authority; store-backed round
// transitions are covered in lycaon/test/wiring/session_ask_round_test.go.
func (f *askBarrierFixture) setAskMode(ask bool) {
	f.mu.Lock()
	f.askMode = ask
	f.mu.Unlock()
}

func (f *askBarrierFixture) pageOutput(t *testing.T, handle string, cursor float64) (string, string, bool) {
	t.Helper()
	tool := &command.CommandOutputTool{Registry: f.Background}
	args := map[string]any{"handle": handle}
	if cursor > 0 {
		args["cursor"] = cursor
	}
	raw, err := f.Invoke(t, t.Context(), "command_output", args, tool.Run)
	testutil.FailErr(t, "run command_output", err)
	var decoded struct {
		Chunks  []bgprocess.OutputChunk `json:"chunks"`
		Running bool                    `json:"running"`
	}
	testutil.FailErr(t, "decode command_output", json.Unmarshal([]byte(raw), &decoded))
	var stdout, stderr strings.Builder
	for _, chunk := range decoded.Chunks {
		if chunk.Stream == "stderr" {
			stderr.WriteString(chunk.Text)
		} else {
			stdout.WriteString(chunk.Text)
		}
	}
	return stdout.String(), stderr.String(), decoded.Running
}

func (f *askBarrierFixture) stopJob(t *testing.T, handle string) toolkit.ProcessStopResult {
	t.Helper()
	tool := &command.CommandStopTool{Registry: f.Background}
	raw, err := f.Invoke(t, t.Context(), "command_stop", map[string]any{"handle": handle}, tool.Run)
	testutil.FailErr(t, "run command_stop", err)
	var res toolkit.ProcessStopResult
	testutil.FailErr(t, "decode command_stop", json.Unmarshal([]byte(raw), &res))
	return res
}

func TestAskExplicitBackgroundJobPagesStopsAndRetainsReadOnlyAuthorityAcrossUpgrade(t *testing.T) {
	t.Run("page_and_stop_in_ask", func(t *testing.T) {
		f := newAskBarrierFixture(t, "ask-job-ready")
		raw, err := f.Call(t, t.Context(), "{ head; echo unreachable; } &")
		testutil.FailErr(t, "launch Ask & job", err)
		out := decodeOutcome(t, raw)
		if out.Status != 0 || out.Refusal != nil {
			t.Fatalf("launch outcome = %+v", out)
		}
		<-f.entered

		jobs := f.Background.ActiveJobs(f.Session)
		if len(jobs) != 1 {
			t.Fatalf("active jobs = %+v, want 1", jobs)
		}
		handle := jobs[0].Handle

		stdout, _, running := f.pageOutput(t, handle, 0)
		if !running || stdout != "ask-job-ready\n" {
			t.Fatalf("command_output running=%t stdout=%q", running, stdout)
		}

		stopRes := f.stopJob(t, handle)
		if !stopRes.StopRequested {
			t.Fatalf("command_stop result = %+v", stopRes)
		}
		_, stopped, err := f.Background.Lifecycle.Wait(t.Context(), f.Session, handle)
		testutil.FailErr(t, "wait stopped & job", err)
		if !stopped {
			t.Fatal("expected stopped=true after command_stop")
		}
		stdoutAfter, _, runningAfter := f.pageOutput(t, handle, 0)
		if runningAfter || strings.Contains(stdoutAfter, "unreachable") {
			t.Fatalf("stopped job ran trailing statement: running=%t stdout=%q", runningAfter, stdoutAfter)
		}
	})

	for _, c := range []struct {
		name     string
		program  string
		wantCode string
	}{
		{
			name:     "write_after_upgrade_refused",
			program:  "{ head; printf 'escaped\\n' | tee escaped.txt; } &",
			wantCode: "COMMAND_EFFECT_NOT_ALLOWED",
		},
		{
			name:     "external_after_upgrade_refused",
			program:  "{ head; external -- /bin/echo outside-after-upgrade; } &",
			wantCode: tools.AskModeActiveCode,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newAskBarrierFixture(t, "ask-job-holding")
			raw, err := f.Call(t, t.Context(), c.program)
			testutil.FailErr(t, "launch Ask & job", err)
			if out := decodeOutcome(t, raw); out.Status != 0 || out.Refusal != nil {
				t.Fatalf("launch outcome = %+v", out)
			}
			<-f.entered

			jobs := f.Background.ActiveJobs(f.Session)
			if len(jobs) != 1 {
				t.Fatalf("active jobs = %+v, want 1", jobs)
			}
			handle := jobs[0].Handle

			stdout, _, running := f.pageOutput(t, handle, 0)
			if !running || stdout != "ask-job-holding\n" {
				t.Fatalf("command_output before upgrade: running=%t stdout=%q", running, stdout)
			}

			// Upgrade session ask -> agent while the & job is running.
			f.setAskMode(false)
			agentRaw, err := f.Call(t, t.Context(), "printf 'agent-write\\n' | tee agent.txt; external -- /bin/echo agent-external")
			testutil.FailErr(t, "run Agent foreground call after upgrade", err)
			agentOut := decodeOutcome(t, agentRaw)
			if agentOut.Status != 0 || agentOut.Refusal != nil || !strings.Contains(agentOut.Stdout, "agent-external") {
				t.Fatalf("Agent call after upgrade failed: %+v", agentOut)
			}

			// Release the Ask-launched & job and verify it still runs under its frozen Ask authority.
			close(f.release)
			exitCode, _, err := f.Background.Lifecycle.Wait(t.Context(), f.Session, handle)
			testutil.FailErr(t, "wait Ask & job completion", err)
			if exitCode == 0 {
				t.Fatalf("expected non-zero exit for refused post-upgrade statement in Ask & job, got %d", exitCode)
			}
			finalStdout, finalStderr, finalRunning := f.pageOutput(t, handle, 0)
			if finalRunning {
				t.Fatal("expected & job to have settled")
			}
			if strings.Contains(finalStdout, "outside-after-upgrade") {
				t.Fatalf("Ask & job executed external binary after upgrade: %q", finalStdout)
			}
			if !strings.Contains(finalStderr, "["+c.wantCode+"]") {
				t.Fatalf("Ask & job stderr = %q, want [%s]", finalStderr, c.wantCode)
			}
			if _, statErr := os.Stat(filepath.Join(f.Dir, "escaped.txt")); !os.IsNotExist(statErr) {
				t.Fatalf("Ask & job wrote escaped.txt after upgrade: err=%v", statErr)
			}
		})
	}
}

func TestAskBudgetContinuedProgramPagesStopsAndRetainsReadOnlyAuthorityAcrossUpgrade(t *testing.T) {
	t.Run("page_and_stop_in_ask", func(t *testing.T) {
		f := newAskBarrierFixture(t, "continued-ask-ready")
		type callRes struct {
			raw string
			err error
		}
		done := make(chan callRes, 1)
		go func() {
			raw, err := f.Call(t, t.Context(), "head; echo unreachable-after-stop")
			done <- callRes{raw, err}
		}()
		<-f.entered
		f.clock.Advance(command.ProgramInlineWait)
		promoted := <-done
		testutil.FailErr(t, "promote foreground Ask program", promoted.err)

		var running command.RunningProgramResult
		testutil.FailErr(t, "decode RunningProgramResult", json.Unmarshal([]byte(promoted.raw), &running))
		if running.State != "running" || running.Handle == "" {
			t.Fatalf("unexpected RunningProgramResult: %+v", running)
		}

		stdout, _, isRunning := f.pageOutput(t, running.Handle, 0)
		if !isRunning || stdout != "continued-ask-ready\n" {
			t.Fatalf("command_output on continued job: running=%t stdout=%q", isRunning, stdout)
		}

		stopRes := f.stopJob(t, running.Handle)
		if !stopRes.StopRequested {
			t.Fatalf("command_stop on continued job: %+v", stopRes)
		}
		_, _, err := f.Background.Lifecycle.Wait(t.Context(), f.Session, running.Handle)
		testutil.FailErr(t, "wait stopped continued job", err)

		finalStdout, _, finalRunning := f.pageOutput(t, running.Handle, 0)
		if finalRunning || strings.Contains(finalStdout, "unreachable-after-stop") {
			t.Fatalf("stopped continued program ran later statement: running=%t stdout=%q", finalRunning, finalStdout)
		}
		if f.clock.Pending() != 0 {
			t.Fatalf("retained %d timers", f.clock.Pending())
		}
	})

	for _, c := range []struct {
		name     string
		program  string
		wantCode string
	}{
		{
			name:     "write_after_upgrade_refused",
			program:  "head; printf 'escaped\\n' | tee continued-escaped.txt",
			wantCode: "COMMAND_EFFECT_NOT_ALLOWED",
		},
		{
			name:     "external_after_upgrade_refused",
			program:  "head; external -- /bin/echo continued-outside",
			wantCode: tools.AskModeActiveCode,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newAskBarrierFixture(t, "continued-holding")
			type callRes struct {
				raw string
				err error
			}
			done := make(chan callRes, 1)
			go func() {
				raw, err := f.Call(t, t.Context(), c.program)
				done <- callRes{raw, err}
			}()
			<-f.entered
			f.clock.Advance(command.ProgramInlineWait)
			promoted := <-done
			testutil.FailErr(t, "promote foreground Ask program", promoted.err)

			var running command.RunningProgramResult
			testutil.FailErr(t, "decode RunningProgramResult", json.Unmarshal([]byte(promoted.raw), &running))
			if running.State != "running" || running.Handle == "" {
				t.Fatalf("unexpected RunningProgramResult: %+v", running)
			}

			stdout, _, isRunning := f.pageOutput(t, running.Handle, 0)
			if !isRunning || stdout != "continued-holding\n" {
				t.Fatalf("command_output before upgrade: running=%t stdout=%q", isRunning, stdout)
			}

			// Upgrade session ask -> agent while the 30s-continued program is running.
			f.setAskMode(false)
			agentRaw, err := f.Call(t, t.Context(), "printf 'agent-write\\n' | tee agent.txt; external -- /bin/echo agent-external")
			testutil.FailErr(t, "run Agent foreground call after upgrade", err)
			agentOut := decodeOutcome(t, agentRaw)
			if agentOut.Status != 0 || agentOut.Refusal != nil || !strings.Contains(agentOut.Stdout, "agent-external") {
				t.Fatalf("Agent call after upgrade failed: %+v", agentOut)
			}

			// Release the continued Ask program and verify its subsequent statements remain clamped to Ask.
			close(f.release)
			exitCode, _, err := f.Background.Lifecycle.Wait(t.Context(), f.Session, running.Handle)
			testutil.FailErr(t, "wait continued Ask program", err)
			if exitCode == 0 {
				t.Fatalf("expected non-zero exit code for refused statement in continued Ask program, got %d", exitCode)
			}
			finalStdout, finalStderr, finalRunning := f.pageOutput(t, running.Handle, 0)
			if finalRunning {
				t.Fatal("expected continued program to have finished")
			}
			if strings.Contains(finalStdout, "continued-outside") {
				t.Fatalf("continued Ask program executed external binary after upgrade: %q", finalStdout)
			}
			if _, statErr := os.Stat(filepath.Join(f.Dir, "continued-escaped.txt")); !os.IsNotExist(statErr) {
				t.Fatalf("continued Ask program wrote continued-escaped.txt after upgrade: err=%v", statErr)
			}
			var finalOutcome command.ProgramOutcome
			lines := strings.Split(strings.TrimSpace(finalStderr), "\n")
			testutil.FailErr(t, "decode final outcome from stderr", json.Unmarshal([]byte(lines[len(lines)-1]), &finalOutcome))
			if finalOutcome.ProgramID != running.ProgramID || finalOutcome.Refusal == nil || finalOutcome.Refusal.Code != c.wantCode {
				t.Fatalf("finalOutcome = %+v (refusal=%+v), want code %s", finalOutcome, finalOutcome.Refusal, c.wantCode)
			}
			if f.clock.Pending() != 0 {
				t.Fatalf("retained %d timers", f.clock.Pending())
			}
		})
	}
}
