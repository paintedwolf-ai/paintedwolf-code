package toolhost

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// Hold the actual pool at the reached input door; the original owner still
// performs capture and raises the original admission refusal.
type constrainedRedirectInput struct {
	commandprogram.InputRedirection
	open func(context.Context, commandprogram.InputDestination) (io.ReadCloser, error)
}

func (c constrainedRedirectInput) OpenInput(ctx context.Context, d commandprogram.InputDestination) (io.ReadCloser, error) {
	return c.open(ctx, d)
}

func TestCommandInputResourceRefusalPreservesActualStopAndPriorEffects(t *testing.T) {
	for _, account := range []string{"working", "state"} {
		t.Run(account, func(t *testing.T) {
			env := newPipeSemanticsEnv(t)
			hints, err := guidance.LoadHintConfigStock()
			if err != nil {
				t.Fatal(err)
			}
			env.Runtime.Executor.Rejections.SetRejectFormatter(guidance.NewStaticRejectFormatter(hints))
			if err := os.WriteFile(filepath.Join(env.Dir, "input"), []byte("unread bytes\n"), 0600); err != nil {
				t.Fatal(err)
			}
			limits, err := toolcontract.CatalogProgramMemoryLimits()
			if err != nil {
				t.Fatal(err)
			}
			var observed *toolrejection.ToolReject
			var original *toolcontract.ProgramMemoryLimitError
			var memory *toolcontract.ProgramMemory
			owners := env.Runtime.Owners
			env.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, pool *toolcontract.ProgramMemory, spill tooloutput.SpillAdmission) (command.ProgramOwners, error) {
				memory = pool
				bound, err := owners(tc, paths, pool, spill)
				if err != nil {
					return bound, err
				}
				redirections := bound.Redirections
				bound.Redirections = func(frame *projectpaths.ProgramPaths) (commandprogram.InputRedirection, commandprogram.OutputPublication) {
					input, output := redirections(frame)
					return constrainedRedirectInput{InputRedirection: input, open: func(ctx context.Context, d commandprogram.InputDestination) (io.ReadCloser, error) {
						usage := pool.InUse()
						var release func()
						var err error
						if account == "working" {
							release, err = pool.Reserve(ctx, limits.Working-usage.Working-((32<<10)-1))
						} else {
							release, err = pool.ReserveProgramState(ctx, limits.State-usage.State-255)
						}
						if err != nil {
							t.Fatal(err)
						}
						defer release()
						reader, err := input.OpenInput(ctx, d)
						observed = toolrejection.AsToolReject(err)
						errors.As(err, &original)
						return reader, err
					}}, output
				}
				return bound, nil
			}
			const source = "printf before > kept\ncat < input\nprintf after > never"
			raw, err := env.Call(t, t.Context(), source)
			if err != nil {
				t.Fatal(err)
			}
			result := decodePipeOutcome(t, raw)
			if observed == nil || original == nil || original.Account != account || observed.Code != "COMMAND_LIMIT_EXCEEDED" || observed.ArgumentValidation {
				t.Fatalf("original capture failure lost: %+v %+v", observed, original)
			}
			maximum := limits.Working
			limitID := "evaluation_memory_bytes"
			if account == "state" {
				maximum = limits.State
				limitID = "program_state_bytes"
			}
			if observed.Data["limit_id"] != limitID || observed.Data["maximum"] != maximum || observed.Data["observed"] != maximum+1 || observed.Data["source_start_byte"] != strings.Index(source, "input") || observed.Data["source_line"] != 2 || observed.Data["source_column"] != 7 {
				t.Fatalf("actual stop facts changed: %+v", observed.Data)
			}
			if result.Refusal == nil || result.Refusal.Code != observed.Code || result.StopCode != observed.Code || result.Status == 0 || result.State == "completed" || result.Stderr != "" || result.Stdout != "" || result.CompletedEffects != 1 || result.UncertainEffects != 0 {
				t.Fatalf("wire result lost actual refusal/effects: %+v", result)
			}
			if kept, err := os.ReadFile(filepath.Join(env.Dir, "kept")); err != nil || string(kept) != "before" {
				t.Fatalf("prior publication %q %v", kept, err)
			}
			if _, err := os.Stat(filepath.Join(env.Dir, "never")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("continuation ran: %v", err)
			}
			details := result.Refusal.Details
			if details["limit_id"] != limitID || details["maximum"] != float64(maximum) || details["observed"] != float64(maximum+1) || details["source_line"] != float64(2) || details["source_column"] != float64(7) || details["source_start_byte"] != float64(strings.Index(source, "input")) || details["native_operation"] != nil {
				t.Fatalf("serialized screened facts changed: %+v", details)
			}
			t.Logf("Actual %s input refusal:\n%s", account, result.Refusal.Feedback)
			// Output retention may retain its bounded per-program index; feedback's
			// own projection and render allowance must have ended after serialization.
			if use := memory.InUse(); use.Working != 0 || use.State > 64<<10 {
				t.Fatalf("feedback admission leaked after delivery: %+v", use)
			}
		})
	}
}

func TestCommandProgramLimitRefusalKeepsTypedRuntimeFacts(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	hints, err := guidance.LoadHintConfigStock()
	if err != nil {
		t.Fatal(err)
	}
	env.Runtime.Executor.Rejections.SetRejectFormatter(guidance.NewStaticRejectFormatter(hints))
	raw, err := env.Call(t, t.Context(), "printf before > kept\nf() { f; }\nf\nprintf after > never")
	if err != nil {
		t.Fatal(err)
	}
	result := decodePipeOutcome(t, raw)
	if result.Refusal == nil || result.Refusal.Code != "COMMAND_LIMIT_EXCEEDED" || result.StopCode != result.Refusal.Code || result.Status == 0 || result.CompletedEffects != 1 || result.UncertainEffects != 0 {
		t.Fatalf("program limit lost actual stop: %+v", result)
	}
	details := result.Refusal.Details
	if details["limit_id"] != "function_depth" || details["maximum"] != float64(32) || details["observed"] != float64(33) || details["resource_unit"] != "frames" || details["source_line"] == nil || details["source_column"] == nil || details["native_operation"] != nil {
		t.Fatalf("program limit facts %+v", details)
	}
	if kept, err := os.ReadFile(filepath.Join(env.Dir, "kept")); err != nil || string(kept) != "before" {
		t.Fatalf("earlier effect %q %v", kept, err)
	}
	if _, err := os.Stat(filepath.Join(env.Dir, "never")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("continuation ran: %v", err)
	}
	t.Logf("Actual program limit feedback:\n%s", result.Refusal.Feedback)
}
