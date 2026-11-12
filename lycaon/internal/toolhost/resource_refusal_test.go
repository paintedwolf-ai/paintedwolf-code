package toolhost

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// Observe the real owner error without replacing the reader, refusal, or result.
type resourceRefusalObserver struct {
	prepare func(context.Context, *projectpaths.ProgramPaths, commandprogram.Dispatch, commandprogram.OperationRequest) (command.NativePrepared, error)
	observe func(*toolrejection.ToolReject)
}

func (o resourceRefusalObserver) Prepare(ctx context.Context, paths *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
	prepared, err := o.prepare(ctx, paths, dispatch, request)
	if err != nil {
		return prepared, err
	}
	invoke := prepared.Invoke
	prepared.Invoke = func(ctx context.Context, admitted tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
		result, err := invoke(ctx, admitted, streams)
		if reject := toolrejection.AsToolReject(err); reject != nil {
			o.observe(reject)
		}
		return result, err
	}
	return prepared, nil
}

func TestCommandResourceRefusalPreservesFactsAndEarlierEffects(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "guidance.LoadHintConfigStock failed", err)
	if err = guidance.ValidateHintConfig(hints); err != nil {
		testutil.FailErr(t, "guidance.ValidateHintConfig failed", err)
	}
	env.Runtime.Executor.Rejections.SetRejectFormatter(guidance.NewStaticRejectFormatter(hints))
	if err := os.WriteFile(filepath.Join(env.Dir, "record"), bytes.Repeat([]byte{'x'}, (4<<20)+1), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	owners := env.Runtime.Owners
	var refusal *toolrejection.ToolReject
	var observed int
	env.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, admission tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		bound, err := owners(tc, paths, memory, admission)
		if err != nil {
			return bound, err
		}
		native := bound.Natives["cut"]
		if native.Prepare == nil {
			t.Fatal("cut has no native owner to observe")
		}
		bound.Natives["cut"] = command.NativeOperation{
			Tool:        native.Tool,
			ClosedForms: native.ClosedForms,
			Prepare: resourceRefusalObserver{prepare: native.Prepare, observe: func(reject *toolrejection.ToolReject) {
				refusal = reject
				observed++
			}}.Prepare,
		}
		return bound, nil
	}
	out, err := env.Call(t, t.Context(), `printf before > kept; cut -f1 record; printf after > never`)
	testutil.FailErr(t, "env.Call failed", err)
	result := decodePipeOutcome(t, out)
	if observed != 1 || refusal == nil || refusal.Code != "COMMAND_LIMIT_EXCEEDED" || refusal.Data["limit_id"] != "record_bytes" {
		t.Fatalf("owner refusal %d %+v", observed, refusal)
	}
	if maximum, ok := refusal.Data["maximum"].(int64); !ok || maximum != 4<<20 {
		t.Fatalf("limit facts %+v", refusal.Data)
	}
	if used, ok := refusal.Data["observed"].(int64); !ok || used <= 4<<20 {
		t.Fatalf("observed limit facts %+v", refusal.Data)
	}
	if result.Refusal == nil || result.Refusal.Code != refusal.Code || result.Refusal.Feedback == "" || result.StopCode != refusal.Code || result.State == "completed" || result.Status == 0 {
		t.Fatalf("delivered refusal %+v", result)
	}
	t.Logf("Delivered resource refusal:\n%s", result.Refusal.Feedback)
	assertCommandRefusalDiagnostic(t, result.Stderr, "cut", "request", refusal.Code)
	if result.Stdout != "" || result.CompletedEffects != 1 || result.UncertainEffects != 0 {
		t.Fatalf("partial outcome %+v", result)
	}
	if kept, err := os.ReadFile(filepath.Join(env.Dir, "kept")); err != nil || string(kept) != "before" {
		t.Fatalf("earlier effect %q %v", kept, err)
	}
	if _, err := os.Stat(filepath.Join(env.Dir, "never")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("continuation ran: %v", err)
	}
}
