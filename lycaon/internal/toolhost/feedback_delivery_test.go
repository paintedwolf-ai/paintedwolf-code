package toolhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestCommandFeedbackDeliveryFailurePreservesActualPartialResult(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, mode := range []string{"capacity", "canceled"} {
			t.Run(map[bool]string{false: "input", true: "native"}[native]+"/"+mode, func(t *testing.T) {
				env := newPipeSemanticsEnv(t)
				hints, err := guidance.LoadHintConfigStock()
				testutil.FailErr(t, "guidance.LoadHintConfigStock failed", err)
				env.Runtime.Executor.Rejections.SetRejectFormatter(guidance.NewStaticRejectFormatter(hints))
				input := []byte("input\n")
				stage := "cat < input"
				if native {
					input = bytes.Repeat([]byte{'x'}, (4<<20)+1)
					stage = "cut -f1 input"
				}
				if err := os.WriteFile(filepath.Join(env.Dir, "input"), input, 0600); err != nil {
					testutil.FailErr(t, "write file", err)
				}
				limits, err := toolcontract.CatalogProgramMemoryLimits()
				testutil.FailErr(t, "toolcontract.CatalogProgramMemoryLimits failed", err)
				var memory *toolcontract.ProgramMemory
				owners := env.Runtime.Owners
				env.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, pool *toolcontract.ProgramMemory, spill tooloutput.SpillAdmission) (command.ProgramOwners, error) {
					memory = pool
					bound, err := owners(tc, paths, pool, spill)
					if err != nil || native {
						return bound, err
					}
					redirections := bound.Redirections
					bound.Redirections = func(frame *projectpaths.ProgramPaths, admit tools.CommandOutputAdmission) (commandprogram.InputRedirection, commandprogram.OutputPublication) {
						input, output := redirections(frame, admit)
						return constrainedRedirectInput{InputRedirection: input, open: func(ctx context.Context, d commandprogram.InputDestination) (io.ReadCloser, error) {
							release, err := pool.Reserve(ctx, limits.Working-pool.InUse().Working-((32<<10)-1))
							testutil.FailErr(t, "pool.Reserve failed", err)
							defer release()
							return input.OpenInput(ctx, d)
						}}, output
					}
					return bound, nil
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var original *toolrejection.ToolReject
				release := func() {}
				evaluate := env.Runtime.Evaluate
				env.Runtime.Evaluate = func(ctx context.Context, plan commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
					result, err := evaluate(ctx, plan, ports)
					original = toolrejection.AsToolReject(err)
					if original == nil || original.Code != "COMMAND_LIMIT_EXCEEDED" {
						t.Fatalf("real owner did not stop at limit: %+v %v", result, err)
					}
					if mode == "capacity" {
						var reserveErr error
						release, reserveErr = memory.ReserveProgramState(t.Context(), limits.State-memory.InUse().State)
						if reserveErr != nil {
							t.Fatal(reserveErr)
						}
					} else {
						cancel()
						<-ctx.Done()
					}
					return result, err
				}
				raw, err := env.Call(t, ctx, "printf before > kept\n"+stage+"\nprintf after > never")
				release()
				if err != nil {
					t.Fatal(err)
				}
				result := decodePipeOutcome(t, raw)
				statement, subject := "<", "input"
				if native {
					statement, subject = "cut", "request"
				}
				stderr := fmt.Sprintf("%s: %s: This command reached its %v limit (%v bytes; maximum %v) [COMMAND_LIMIT_EXCEEDED]\n", statement, subject, original.Data["limit_id"], original.Data["observed"], original.Data["maximum"])
				if result.Refusal == nil || result.Refusal.Code != original.Code || result.Refusal.Feedback != "" || result.Refusal.Details != nil || result.Refusal.DeliveryFailure == nil || result.Refusal.DeliveryFailure.Kind != mode || result.CompletedEffects != 1 || result.UncertainEffects != 0 || result.Status == 0 || result.State == "completed" || result.Stdout != "" || result.Stderr != stderr {
					t.Fatalf("partial result changed: %+v, want stderr %q", result, stderr)
				}
				failure := result.Refusal.DeliveryFailure
				if mode == "capacity" {
					if failure.Capacity == nil || failure.Capacity.LimitID != "program_state_bytes" || failure.Capacity.ResourceUnit != "bytes" || failure.Capacity.Maximum != limits.State || failure.Capacity.Observed <= limits.State {
						t.Fatalf("capacity facts %+v", failure)
					}
				} else if failure.Capacity != nil {
					t.Fatalf("cancellation invented capacity facts %+v", failure)
				}
				if kept, err := os.ReadFile(filepath.Join(env.Dir, "kept")); err != nil || string(kept) != "before" {
					t.Fatalf("prior publication %q %v", kept, err)
				}
				if _, err := os.Stat(filepath.Join(env.Dir, "never")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("continuation ran: %v", err)
				}
				if use := memory.InUse(); use.Working != 0 || use.State > 64<<10 {
					t.Fatalf("delivery leaked admission: %+v", use)
				}
			})
		}
	}
}
